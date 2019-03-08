package integration_tests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/CS-SI/SafeScale/integration_tests/enums/Providers"
	"github.com/stretchr/testify/require"
)

func Docker(t *testing.T, provider Providers.Enum) {
	Setup(t, provider)

	names := GetNames("Docker", 0, 0, 0, 0, 1, 0)
	names.TearDown()
	defer names.TearDown()

	out, err := GetOutput("safescale network create " + names.Networks[0] + " --cidr 168.192.100.0/24")
	require.Nil(t, err)

	out, err = GetOutput("safescale ssh run gw-" + names.Networks[0] + " -c \"uptime\"")
	fmt.Print(out)
	require.Nil(t, err)
	require.True(t, strings.Contains(out, " user"))

	out, err = GetOutput("safescale host check-feature gw-" + names.Networks[0] + " docker")
	require.NotNil(t, err)

	out, err = GetOutput("safescale host add-feature gw-" + names.Networks[0] + " docker")
	require.Nil(t, err)

	out, err = GetOutput("safescale ssh run gw-" + names.Networks[0] + " -c \"docker ps\"")
	fmt.Print(out)
	require.Nil(t, err)
	require.True(t, strings.Contains(out, "CONTAINER"))

	out, err = GetOutput("safescale host check-feature gw-" + names.Networks[0] + " docker")
	require.Nil(t, err)

	out, err = GetOutput("safescale host delete-feature gw-" + names.Networks[0] + " docker")
	require.Nil(t, err)

	out, err = GetOutput("safescale host check-feature gw-" + names.Networks[0] + " docker")
	require.NotNil(t, err)

	out, err = GetOutput("safescale ssh run gw-" + names.Networks[0] + " -c \"docker ps\"")
	fmt.Print(out)
	require.NotNil(t, err)
	require.False(t, strings.Contains(out, "CONTAINER"))
}

func DockerNotGateway(t *testing.T, provider Providers.Enum) {
	Setup(t, provider)

	names := GetNames("DockerNotGateway", 0, 0, 0, 1, 1, 0)
	names.TearDown()
	defer names.TearDown()

	out, err := GetOutput("safescale network create " + names.Networks[0] + " --cidr 168.192.101.0/24")
	require.Nil(t, err)

	out, err = GetOutput("safescale ssh run gw-" + names.Networks[0] + " -c \"uptime\"")
	fmt.Print(out)
	require.Nil(t, err)
	require.True(t, strings.Contains(out, " user"))

	out, err = GetOutput("safescale host create " + names.Hosts[0] + " --net " + names.Networks[0])
	require.Nil(t, err)

	out, err = GetOutput("safescale host check-feature " + names.Hosts[0] + " docker")
	require.NotNil(t, err)

	out, err = GetOutput("safescale host add-feature " + names.Hosts[0] + " docker")
	require.Nil(t, err)

	out, err = GetOutput("safescale ssh run " + names.Hosts[0] + " -c \"docker ps\"")
	fmt.Print(out)
	require.Nil(t, err)
	require.True(t, strings.Contains(out, "CONTAINER"))

	out, err = GetOutput("safescale host check-feature " + names.Hosts[0] + " docker")
	require.Nil(t, err)

	out, err = GetOutput("safescale host delete-feature " + names.Hosts[0] + " docker")
	require.Nil(t, err)

	out, err = GetOutput("safescale host check-feature " + names.Hosts[0] + " docker")
	require.NotNil(t, err)

	out, err = GetOutput("safescale ssh run " + names.Hosts[0] + " -c \"docker ps\"")
	fmt.Print(out)
	require.NotNil(t, err)
	require.False(t, strings.Contains(out, "CONTAINER"))
}

func DockerCompose(t *testing.T, provider Providers.Enum) {
	Setup(t, provider)

	names := GetNames("DockerCompose", 0, 0, 0, 0, 1, 0)
	names.TearDown()
	defer names.TearDown()

	out, err := GetOutput("safescale network create " + names.Networks[0] + " --cidr 168.192.102.0/24")
	require.Nil(t, err)

	out, err = GetOutput("safescale ssh run gw-" + names.Networks[0] + " -c \"uptime\"")
	fmt.Print(out)
	require.Nil(t, err)
	require.True(t, strings.Contains(out, " user"))

	out, err = GetOutput("safescale host check-feature gw-" + names.Networks[0] + " docker-compose")
	require.NotNil(t, err)

	out, err = GetOutput("safescale host add-feature gw-" + names.Networks[0] + " docker-compose")
	require.Nil(t, err)

	out, err = GetOutput("safescale ssh run gw-" + names.Networks[0] + " -c \"docker-compose -v\"")
	fmt.Print(out)
	require.Nil(t, err)

	out, err = GetOutput("safescale host check-feature gw-" + names.Networks[0] + " docker-compose")
	require.Nil(t, err)

	out, err = GetOutput("safescale host delete-feature gw-" + names.Networks[0] + " docker-compose")
	require.Nil(t, err)

	out, err = GetOutput("safescale host check-feature gw-" + names.Networks[0] + " docker-compose")
	require.NotNil(t, err)

	out, err = GetOutput("safescale ssh run gw-" + names.Networks[0] + " -c \"docker-compose -v\"")
	fmt.Print(out)
	require.NotNil(t, err)
}

func RemoteDesktop(t *testing.T, provider Providers.Enum) {
	Setup(t, provider)

	names := GetNames("RemoteDesktop", 0, 0, 0, 1, 1, 0)
	names.TearDown()
	defer names.TearDown()

	out, err := GetOutput("safescale network create " + names.Networks[0] + " --cidr 168.192.103.0/24")
	require.Nil(t, err)

	out, err = GetOutput("safescale host create " + names.Hosts[0] + " --public --net " + names.Networks[0])
	require.Nil(t, err)
	host := HostInfo{}
	json.Unmarshal([]byte(out), &host)

	out, err = GetOutput("safescale host check-feature --param Password=SafeScale " + names.Hosts[0] + " remotedesktop")
	require.NotNil(t, err)

	out, err = GetOutput("safescale host add-feature --skip-proxy --param Password=SafeScale " + names.Hosts[0] + " remotedesktop")
	require.Nil(t, err)

	//TODO : try to connect to the host through guacamole?
	out, err = GetOutput("wget " + host.PUBLIC_IP + ":9080/guacamole")
	fmt.Print(out)
	require.Nil(t, err)

	fmt.Println(names.Hosts[0])
	out, err = GetOutput("safescale host check-feature --param Password=SafeScale " + names.Hosts[0] + " remotedesktop")
	fmt.Println(out)
	require.Nil(t, err)

	out, err = GetOutput("safescale host delete-feature --param Password=SafeScale " + names.Hosts[0] + " remotedesktop")
	require.Nil(t, err)

	out, err = GetOutput("safescale host check-feature --param Password=SafeScale " + names.Hosts[0] + " remotedesktop")
	require.NotNil(t, err)

	out, err = GetOutput("wget " + host.PUBLIC_IP + ":9080/guacamole")
	fmt.Print(out)
	require.NotNil(t, err)
}

func ReverseProxy(t *testing.T, provider Providers.Enum) {
	Setup(t, provider)

	names := GetNames("ReverseProxy", 0, 0, 0, 0, 1, 0)
	names.TearDown()
	defer names.TearDown()

	out, err := GetOutput("safescale network create " + names.Networks[0] + " --cidr 168.192.104.0/24")
	require.Nil(t, err)

	out, err = GetOutput("safescale host check-feature gw-" + names.Networks[0] + " reverseproxy")
	require.NotNil(t, err)

	out, err = GetOutput("safescale host add-feature gw-" + names.Networks[0] + " reverseproxy")
	require.Nil(t, err)

	out, err = GetOutput("safescale ssh run gw-" + names.Networks[0] + " -c \"curl -Ssl -I -k https://localhost:8444/ 2>&1 | grep \\\"HTTP/1.1 200 OK\\\"\"")
	fmt.Print(out)
	require.Nil(t, err)

	out, err = GetOutput("safescale host check-feature gw-" + names.Networks[0] + " reverseproxy")
	require.Nil(t, err)

	out, err = GetOutput("safescale host delete-feature gw-" + names.Networks[0] + " reverseproxy")
	require.Nil(t, err)

	out, err = GetOutput("safescale host check-feature gw-" + names.Networks[0] + " reverseproxy")
	require.NotNil(t, err)

	out, err = GetOutput("safescale ssh run gw-" + names.Networks[0] + " -c \"curl -Ssl -I -k https://localhost:8444/ 2>&1 | grep \\\"HTTP/1.1 200 OK\\\"\"")
	fmt.Print(out)
	require.NotNil(t, err)
}
