# -*- bash -*-

podman container inspect two_networks_con1_1 --format '{{len .NetworkSettings.Networks}}'
is "$output" "2" "Container is connected to both networks"
podman container inspect two_networks_con1_1 --format '{{.NetworkSettings.Networks}}'
like "$output" "two_networks_net1" "First network name exists"
like "$output" "two_networks_net2" "Second network name exists"
