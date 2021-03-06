package bindings

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	jsoniter "github.com/json-iterator/go"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"k8s.io/client-go/util/homedir"
)

var (
	BasePath = &url.URL{
		Scheme: "http",
		Host:   "d",
		Path:   "/v" + APIVersion.String() + "/libpod",
	}
	passPhrase []byte
	phraseSync sync.Once
)

type APIResponse struct {
	*http.Response
	Request *http.Request
}

type Connection struct {
	Uri    *url.URL
	Client *http.Client
}

type valueKey string

const (
	clientKey = valueKey("Client")
)

// GetClient from context build by NewConnection()
func GetClient(ctx context.Context) (*Connection, error) {
	c, ok := ctx.Value(clientKey).(*Connection)
	if !ok {
		return nil, errors.Errorf("ClientKey not set in context")
	}
	return c, nil
}

// JoinURL elements with '/'
func JoinURL(elements ...string) string {
	return "/" + strings.Join(elements, "/")
}

func NewConnection(ctx context.Context, uri string) (context.Context, error) {
	return NewConnectionWithIdentity(ctx, uri, "")
}

// NewConnection takes a URI as a string and returns a context with the
// Connection embedded as a value.  This context needs to be passed to each
// endpoint to work correctly.
//
// A valid URI connection should be scheme://
// For example tcp://localhost:<port>
// or unix:///run/podman/podman.sock
// or ssh://<user>@<host>[:port]/run/podman/podman.sock?secure=True
func NewConnectionWithIdentity(ctx context.Context, uri string, passPhrase string, identities ...string) (context.Context, error) {
	var (
		err    error
		secure bool
	)
	if v, found := os.LookupEnv("CONTAINER_HOST"); found && uri == "" {
		uri = v
	}

	if v, found := os.LookupEnv("CONTAINER_SSHKEY"); found && len(identities) == 0 {
		identities = append(identities, v)
	}

	if v, found := os.LookupEnv("CONTAINER_PASSPHRASE"); found && passPhrase == "" {
		passPhrase = v
	}

	_url, err := url.Parse(uri)
	if err != nil {
		return nil, errors.Wrapf(err, "Value of CONTAINER_HOST is not a valid url: %s", uri)
	}
	// TODO Fill in missing defaults for _url...

	// Now we setup the http Client to use the connection above
	var connection Connection
	switch _url.Scheme {
	case "ssh":
		secure, err = strconv.ParseBool(_url.Query().Get("secure"))
		if err != nil {
			secure = false
		}
		connection, err = sshClient(_url, secure, passPhrase, identities...)
	case "unix":
		if !strings.HasPrefix(uri, "unix:///") {
			// autofix unix://path_element vs unix:///path_element
			_url.Path = JoinURL(_url.Host, _url.Path)
			_url.Host = ""
		}
		connection, err = unixClient(_url)
	case "tcp":
		if !strings.HasPrefix(uri, "tcp://") {
			return nil, errors.New("tcp URIs should begin with tcp://")
		}
		connection, err = tcpClient(_url)
	default:
		return nil, errors.Errorf("'%s' is not a supported schema", _url.Scheme)
	}
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to create %sClient", _url.Scheme)
	}

	ctx = context.WithValue(ctx, clientKey, &connection)
	if err := pingNewConnection(ctx); err != nil {
		return nil, err
	}
	return ctx, nil
}

func tcpClient(_url *url.URL) (Connection, error) {
	connection := Connection{
		Uri: _url,
	}
	connection.Client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("tcp", _url.Host)
			},
			DisableCompression: true,
		},
	}
	return connection, nil
}

// pingNewConnection pings to make sure the RESTFUL service is up
// and running. it should only be used when initializing a connection
func pingNewConnection(ctx context.Context) error {
	client, err := GetClient(ctx)
	if err != nil {
		return err
	}
	// the ping endpoint sits at / in this case
	response, err := client.DoRequest(nil, http.MethodGet, "../../../_ping", nil, nil)
	if err != nil {
		return err
	}

	if response.StatusCode == http.StatusOK {
		versionHdr := response.Header.Get("Libpod-API-Version")
		if versionHdr == "" {
			logrus.Info("Service did not provide Libpod-API-Version Header")
			return nil
		}
		versionSrv, err := semver.ParseTolerant(versionHdr)
		if err != nil {
			return err
		}

		switch APIVersion.Compare(versionSrv) {
		case -1, 0:
			// Server's job when Client version is equal or older
			return nil
		case 1:
			return errors.Errorf("server API version is too old. Client %q server %q", APIVersion.String(), versionSrv.String())
		}
	}
	return errors.Errorf("ping response was %q", response.StatusCode)
}

func sshClient(_url *url.URL, secure bool, passPhrase string, identities ...string) (Connection, error) {
	var authMethods []ssh.AuthMethod

	for _, i := range identities {
		auth, err := publicKey(i, []byte(passPhrase))
		if err != nil {
			fmt.Fprint(os.Stderr, errors.Wrapf(err, "failed to parse identity %q", i).Error()+"\n")
			continue
		}
		authMethods = append(authMethods, auth)
	}

	if sock, found := os.LookupEnv("SSH_AUTH_SOCK"); found {
		logrus.Debugf("Found SSH_AUTH_SOCK %q, ssh-agent signer enabled", sock)

		c, err := net.Dial("unix", sock)
		if err != nil {
			return Connection{}, err
		}
		a := agent.NewClient(c)
		authMethods = append(authMethods, ssh.PublicKeysCallback(a.Signers))
	}

	if pw, found := _url.User.Password(); found {
		authMethods = append(authMethods, ssh.Password(pw))
	}

	callback := ssh.InsecureIgnoreHostKey()
	if secure {
		key := hostKey(_url.Hostname())
		if key != nil {
			callback = ssh.FixedHostKey(key)
		}
	}

	port := _url.Port()
	if port == "" {
		port = "22"
	}

	bastion, err := ssh.Dial("tcp",
		net.JoinHostPort(_url.Hostname(), port),
		&ssh.ClientConfig{
			User:            _url.User.Username(),
			Auth:            authMethods,
			HostKeyCallback: callback,
			HostKeyAlgorithms: []string{
				ssh.KeyAlgoRSA,
				ssh.KeyAlgoDSA,
				ssh.KeyAlgoECDSA256,
				ssh.KeyAlgoECDSA384,
				ssh.KeyAlgoECDSA521,
				ssh.KeyAlgoED25519,
			},
			Timeout: 5 * time.Second,
		},
	)
	if err != nil {
		return Connection{}, errors.Wrapf(err, "Connection to bastion host (%s) failed.", _url.String())
	}

	connection := Connection{Uri: _url}
	connection.Client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return bastion.Dial("unix", _url.Path)
			},
		}}
	return connection, nil
}

func unixClient(_url *url.URL) (Connection, error) {
	connection := Connection{Uri: _url}
	connection.Client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", _url.Path)
			},
			DisableCompression: true,
		},
	}
	return connection, nil
}

// DoRequest assembles the http request and returns the response
func (c *Connection) DoRequest(httpBody io.Reader, httpMethod, endpoint string, queryParams url.Values, header map[string]string, pathValues ...string) (*APIResponse, error) {
	var (
		err      error
		response *http.Response
	)
	safePathValues := make([]interface{}, len(pathValues))
	// Make sure path values are http url safe
	for i, pv := range pathValues {
		safePathValues[i] = url.PathEscape(pv)
	}
	// Lets eventually use URL for this which might lead to safer
	// usage
	safeEndpoint := fmt.Sprintf(endpoint, safePathValues...)
	e := BasePath.String() + safeEndpoint
	req, err := http.NewRequest(httpMethod, e, httpBody)
	if err != nil {
		return nil, err
	}
	if len(queryParams) > 0 {
		req.URL.RawQuery = queryParams.Encode()
	}
	for key, val := range header {
		req.Header.Set(key, val)
	}
	req = req.WithContext(context.WithValue(context.Background(), clientKey, c))
	// Give the Do three chances in the case of a comm/service hiccup
	for i := 0; i < 3; i++ {
		response, err = c.Client.Do(req) // nolint
		if err == nil {
			break
		}
		time.Sleep(time.Duration(i*100) * time.Millisecond)
	}
	return &APIResponse{response, req}, err
}

// FiltersToString converts our typical filter format of a
// map[string][]string to a query/html safe string.
func FiltersToString(filters map[string][]string) (string, error) {
	lowerCaseKeys := make(map[string][]string)
	for k, v := range filters {
		lowerCaseKeys[strings.ToLower(k)] = v
	}
	return jsoniter.MarshalToString(lowerCaseKeys)
}

// IsInformation returns true if the response code is 1xx
func (h *APIResponse) IsInformational() bool {
	return h.Response.StatusCode/100 == 1
}

// IsSuccess returns true if the response code is 2xx
func (h *APIResponse) IsSuccess() bool {
	return h.Response.StatusCode/100 == 2
}

// IsRedirection returns true if the response code is 3xx
func (h *APIResponse) IsRedirection() bool {
	return h.Response.StatusCode/100 == 3
}

// IsClientError returns true if the response code is 4xx
func (h *APIResponse) IsClientError() bool {
	return h.Response.StatusCode/100 == 4
}

// IsServerError returns true if the response code is 5xx
func (h *APIResponse) IsServerError() bool {
	return h.Response.StatusCode/100 == 5
}

func publicKey(path string, passphrase []byte) (ssh.AuthMethod, error) {
	key, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); !ok {
			return nil, err
		}
		if len(passphrase) == 0 {
			phraseSync.Do(promptPassphrase)
			passphrase = passPhrase
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, passphrase)
		if err != nil {
			return nil, err
		}
	}
	return ssh.PublicKeys(signer), nil
}

func promptPassphrase() {
	phrase, err := readPassword("Key Passphrase: ")
	if err != nil {
		passPhrase = []byte{}
		return
	}
	passPhrase = phrase
}

func hostKey(host string) ssh.PublicKey {
	// parse OpenSSH known_hosts file
	// ssh or use ssh-keyscan to get initial key
	knownHosts := filepath.Join(homedir.HomeDir(), ".ssh", "known_hosts")
	fd, err := os.Open(knownHosts)
	if err != nil {
		logrus.Error(err)
		return nil
	}

	scanner := bufio.NewScanner(fd)
	for scanner.Scan() {
		_, hosts, key, _, _, err := ssh.ParseKnownHosts(scanner.Bytes())
		if err != nil {
			logrus.Errorf("Failed to parse known_hosts: %s", scanner.Text())
			continue
		}

		for _, h := range hosts {
			if h == host {
				return key
			}
		}
	}

	return nil
}
