package controller

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gravitl/netmaker/database"
	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/logic"
	"github.com/gravitl/netmaker/models"
	"github.com/gravitl/netmaker/servercfg"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/exp/slog"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// swagger:route PUT /api/v1/nodes/migrate nodes migrateNode
//
// Used to migrate a legacy node.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: nodeJoinResponse
func migrate(w http.ResponseWriter, r *http.Request) {
	data := models.MigrationData{}
	host := models.Host{}
	node := models.Node{}
	nodes := []models.Node{}
	server := models.ServerConfig{}
	err := json.NewDecoder(r.Body).Decode(&data)
	if err != nil {
		logger.Log(0, r.Header.Get("user"), "error decoding request body: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}
	for i, legacy := range data.LegacyNodes {
		record, err := database.FetchRecord(database.NODES_TABLE_NAME, legacy.ID)
		if err != nil {
			slog.Error("legacy node not found", "error", err)
			logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("legacy node not found %w", err), "badrequest"))
			return
		}
		var legacyNode models.LegacyNode
		if err = json.Unmarshal([]byte(record), &legacyNode); err != nil {
			slog.Error("decoding legacy node", "errror", err)
			logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("decode legacy node %w", err), "badrequest"))
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(legacyNode.Password), []byte(legacy.Password)); err != nil {
			slog.Error("legacy node invalid password", "error", err)
			logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("invalid password %w", err), "unauthorized"))
			return
		}
		if i == 0 {
			host, node = convertLegacyHostNode(legacyNode)
			host.Name = data.HostName
			if err := logic.CreateHost(&host); err != nil {
				slog.Error("create host", "error", err)
				logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
				return
			}
			server = servercfg.GetServerInfo()
			if servercfg.GetBrokerType() == servercfg.EmqxBrokerType {
				server.MQUserName = host.ID.String()
			}
			key, keyErr := logic.RetrievePublicTrafficKey()
			if keyErr != nil {
				slog.Error("retrieving traffickey", "error", err)
				logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
				return
			}
			server.TrafficKey = key
		} else {
			node = convertLegacyNode(legacyNode, host.ID)
		}
		if err := logic.UpsertNode(&node); err != nil {
			slog.Error("update node", "error", err)
			continue
		}
		slog.Info("")
		nodes = append(nodes, node)
	}
	response := models.HostPull{
		Host:         host,
		Nodes:        nodes,
		ServerConfig: server,
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(&response)

	slog.Info("migrated nodes")
}

func convertLegacyHostNode(legacy models.LegacyNode) (models.Host, models.Node) {
	//convert host
	host := models.Host{}
	host.ID = uuid.New()
	host.IPForwarding = models.ParseBool(legacy.IPForwarding)
	host.AutoUpdate = servercfg.AutoUpdateEnabled()
	host.Interface = "netmaker"
	host.ListenPort = int(legacy.ListenPort)
	host.MTU = int(legacy.MTU)
	host.PublicKey, _ = wgtypes.ParseKey(legacy.PublicKey)
	host.MacAddress = net.HardwareAddr(legacy.MacAddress)
	host.TrafficKeyPublic = legacy.TrafficKeys.Mine
	updAddr, err := net.ResolveUDPAddr("udp", legacy.InternetGateway)
	if err == nil {
		host.InternetGateway = *updAddr
	}
	host.Nodes = append([]string{}, legacy.ID)
	host.Interfaces = legacy.Interfaces
	//host.DefaultInterface = legacy.Defaul
	host.EndpointIP = net.ParseIP(legacy.Endpoint)
	host.IsDocker = models.ParseBool(legacy.IsDocker)
	host.IsK8S = models.ParseBool(legacy.IsK8S)
	host.IsStatic = models.ParseBool(legacy.IsStatic)
	node := convertLegacyNode(legacy, host.ID)
	return host, node
}

func convertLegacyNode(legacy models.LegacyNode, hostID uuid.UUID) models.Node {
	//convert node
	node := models.Node{}
	node.ID, _ = uuid.Parse(legacy.ID)
	node.HostID = hostID
	node.Network = legacy.Network
	_, cidr4, err := net.ParseCIDR(legacy.NetworkSettings.AddressRange)
	if err == nil {
		node.Address = *cidr4
	}
	_, cidr6, err := net.ParseCIDR(legacy.NetworkSettings.AddressRange6)
	if err == nil {
		node.Address6 = *cidr6
	}
	node.Server = legacy.Server
	node.Connected = models.ParseBool(legacy.Connected)
	node.Address = net.IPNet{
		IP:   net.ParseIP(legacy.Address),
		Mask: cidr4.Mask,
	}
	node.Address6 = net.IPNet{
		IP:   net.ParseIP(legacy.Address6),
		Mask: cidr6.Mask,
	}
	node.Action = models.NODE_NOOP
	node.LocalAddress = net.IPNet{
		IP: net.ParseIP(legacy.LocalAddress),
	}
	node.IsEgressGateway = models.ParseBool(legacy.IsEgressGateway)
	node.EgressGatewayRanges = legacy.EgressGatewayRanges
	node.IsIngressGateway = models.ParseBool(legacy.IsIngressGateway)
	node.IsRelayed = models.ParseBool(legacy.IsRelayed)
	node.IsRelay = models.ParseBool(legacy.IsRelay)
	node.RelayedNodes = legacy.RelayAddrs
	node.DNSOn = models.ParseBool(legacy.DNSOn)
	node.PersistentKeepalive = time.Duration(legacy.PersistentKeepalive)
	node.LastModified = time.Now()
	node.ExpirationDateTime, _ = time.Parse(strconv.Itoa(int(legacy.ExpirationDateTime)), "0")
	node.EgressGatewayNatEnabled = models.ParseBool(legacy.EgressGatewayNatEnabled)
	node.EgressGatewayRequest = legacy.EgressGatewayRequest
	node.IngressGatewayRange = legacy.IngressGatewayRange
	node.IngressGatewayRange6 = legacy.IngressGatewayRange6
	node.DefaultACL = legacy.DefaultACL
	node.OwnerID = legacy.OwnerID
	node.FailoverNode, _ = uuid.Parse(legacy.FailoverNode)
	node.Failover = models.ParseBool(legacy.Failover)
	return node
}
