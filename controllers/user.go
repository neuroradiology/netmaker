package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/gravitl/netmaker/auth"
	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/logic"
	"github.com/gravitl/netmaker/models"
	"github.com/gravitl/netmaker/servercfg"
	"golang.org/x/exp/slog"
)

var (
	upgrader = websocket.Upgrader{}
)

func userHandlers(r *mux.Router) {
	r.HandleFunc("/api/users/adm/hassuperadmin", hasSuperAdmin).Methods(http.MethodGet)
	r.HandleFunc("/api/users/adm/createsuperadmin", createSuperAdmin).Methods(http.MethodPost)
	r.HandleFunc("/api/users/adm/transfersuperadmin/{username}", logic.SecurityCheck(true, http.HandlerFunc(transferSuperAdmin))).Methods(http.MethodPost)
	r.HandleFunc("/api/users/adm/authenticate", authenticateUser).Methods(http.MethodPost)
	r.HandleFunc("/api/users/{username}/remote_access_gw/{remote_access_gateway_id}", logic.SecurityCheck(true, http.HandlerFunc(attachUserToRemoteAccessGw))).Methods(http.MethodPost)
	r.HandleFunc("/api/users/{username}/remote_access_gw/{remote_access_gateway_id}", logic.SecurityCheck(true, http.HandlerFunc(removeUserFromRemoteAccessGW))).Methods(http.MethodDelete)
	r.HandleFunc("/api/users/{username}/remote_access_gw", logic.SecurityCheck(false, logic.ContinueIfUserMatch(http.HandlerFunc(getUserRemoteAccessGws)))).Methods(http.MethodGet)
	r.HandleFunc("/api/users/{username}", logic.SecurityCheck(true, http.HandlerFunc(updateUser))).Methods(http.MethodPut)
	r.HandleFunc("/api/users/{username}", logic.SecurityCheck(true, checkFreeTierLimits(limitChoiceUsers, http.HandlerFunc(createUser)))).Methods(http.MethodPost)
	r.HandleFunc("/api/users/{username}", logic.SecurityCheck(true, http.HandlerFunc(deleteUser))).Methods(http.MethodDelete)
	r.HandleFunc("/api/users/{username}", logic.SecurityCheck(false, logic.ContinueIfUserMatch(http.HandlerFunc(getUser)))).Methods(http.MethodGet)
	r.HandleFunc("/api/users", logic.SecurityCheck(true, http.HandlerFunc(getUsers))).Methods(http.MethodGet)
	r.HandleFunc("/api/oauth/login", auth.HandleAuthLogin).Methods(http.MethodGet)
	r.HandleFunc("/api/oauth/callback", auth.HandleAuthCallback).Methods(http.MethodGet)
	r.HandleFunc("/api/oauth/headless", auth.HandleHeadlessSSO)
	r.HandleFunc("/api/oauth/register/{regKey}", auth.RegisterHostSSO).Methods(http.MethodGet)
}

// swagger:route POST /api/users/adm/authenticate user authenticateUser
//
// Node authenticates using its password and retrieves a JWT for authorization.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: successResponse
func authenticateUser(response http.ResponseWriter, request *http.Request) {

	// Auth request consists of Mac Address and Password (from node that is authorizing
	// in case of Master, auth is ignored and mac is set to "mastermac"
	var authRequest models.UserAuthParams
	var errorResponse = models.ErrorResponse{
		Code: http.StatusInternalServerError, Message: "W1R3: It's not you it's me.",
	}

	if !servercfg.IsBasicAuthEnabled() {
		logic.ReturnErrorResponse(response, request, logic.FormatError(fmt.Errorf("basic auth is disabled"), "badrequest"))
		return
	}

	decoder := json.NewDecoder(request.Body)
	decoderErr := decoder.Decode(&authRequest)
	defer request.Body.Close()
	if decoderErr != nil {
		logger.Log(0, "error decoding request body: ",
			decoderErr.Error())
		logic.ReturnErrorResponse(response, request, errorResponse)
		return
	}
	username := authRequest.UserName
	jwt, err := logic.VerifyAuthRequest(authRequest)
	if err != nil {
		logger.Log(0, username, "user validation failed: ",
			err.Error())
		logic.ReturnErrorResponse(response, request, logic.FormatError(err, "badrequest"))
		return
	}

	if jwt == "" {
		// very unlikely that err is !nil and no jwt returned, but handle it anyways.
		logger.Log(0, username, "jwt token is empty")
		logic.ReturnErrorResponse(response, request, logic.FormatError(errors.New("no token returned"), "internal"))
		return
	}

	var successResponse = models.SuccessResponse{
		Code:    http.StatusOK,
		Message: "W1R3: Device " + username + " Authorized",
		Response: models.SuccessfulUserLoginResponse{
			AuthToken: jwt,
			UserName:  username,
		},
	}
	// Send back the JWT
	successJSONResponse, jsonError := json.Marshal(successResponse)

	if jsonError != nil {
		logger.Log(0, username,
			"error marshalling resp: ", err.Error())
		logic.ReturnErrorResponse(response, request, errorResponse)
		return
	}
	logger.Log(2, username, "was authenticated")
	response.Header().Set("Content-Type", "application/json")
	response.Write(successJSONResponse)
}

// swagger:route GET /api/users/adm/hassuperadmin user hasSuperAdmin
//
// Checks whether the server has an admin.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: successResponse
func hasSuperAdmin(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "application/json")

	hasSuperAdmin, err := logic.HasSuperAdmin()
	if err != nil {
		logger.Log(0, "failed to check for admin: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	}

	json.NewEncoder(w).Encode(hasSuperAdmin)

}

// swagger:route GET /api/users/{username} user getUser
//
// Get an individual user.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: userBodyResponse
func getUser(w http.ResponseWriter, r *http.Request) {
	// set header.
	w.Header().Set("Content-Type", "application/json")

	var params = mux.Vars(r)
	usernameFetched := params["username"]
	user, err := logic.GetReturnUser(usernameFetched)

	if err != nil {
		logger.Log(0, usernameFetched, "failed to fetch user: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	}
	logger.Log(2, r.Header.Get("user"), "fetched user", usernameFetched)
	json.NewEncoder(w).Encode(user)
}

// swagger:route POST /api/users/{username}/remote_access_gw user attachUserToRemoteAccessGateway
//
// Attach User to a remote access gateway.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: userBodyResponse
func attachUserToRemoteAccessGw(w http.ResponseWriter, r *http.Request) {
	// set header.
	w.Header().Set("Content-Type", "application/json")

	var params = mux.Vars(r)
	username := params["username"]
	remoteGwID := params["remote_access_gateway_id"]
	if username == "" || remoteGwID == "" {
		logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("required params `username` and `remote_access_gateway_id`"), "badrequest"))
		return
	}
	user, err := logic.GetUser(username)
	if err != nil {
		slog.Error("failed to fetch user: ", "username", username, "error", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("failed to fetch user %s, error: %v", username, err), "badrequest"))
		return
	}
	if user.IsAdmin || user.IsSuperAdmin {
		logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("superadmins/admins have access to all gateways"), "badrequest"))
		return
	}
	node, err := logic.GetNodeByID(remoteGwID)
	if err != nil {
		slog.Error("failed to fetch gateway node", "nodeID", remoteGwID, "error", err)
		logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("failed to fetch remote access gateway node, error: %v", err), "badrequest"))
		return
	}
	if !node.IsIngressGateway {
		logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("node is not a remote access gateway"), "badrequest"))
		return
	}
	if user.RemoteGwIDs == nil {
		user.RemoteGwIDs = make(map[string]struct{})
	}
	user.RemoteGwIDs[node.ID.String()] = struct{}{}
	err = logic.UpsertUser(*user)
	if err != nil {
		slog.Error("failed to update user's gateways", "user", username, "error", err)
		logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("failed to fetch remote access gateway node,error: %v", err), "badrequest"))
		return
	}

	json.NewEncoder(w).Encode(logic.ToReturnUser(*user))
}

// swagger:route DELETE /api/users/{username}/remote_access_gw user removeUserFromRemoteAccessGW
//
// Attach User to a remote access gateway.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: userBodyResponse
func removeUserFromRemoteAccessGW(w http.ResponseWriter, r *http.Request) {
	// set header.
	w.Header().Set("Content-Type", "application/json")

	var params = mux.Vars(r)
	username := params["username"]
	remoteGwID := params["remote_access_gateway_id"]
	if username == "" || remoteGwID == "" {
		logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("required params `username` and `remote_access_gateway_id`"), "badrequest"))
		return
	}
	user, err := logic.GetUser(username)
	if err != nil {
		logger.Log(0, username, "failed to fetch user: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("failed to fetch user %s, error: %v", username, err), "badrequest"))
		return
	}
	delete(user.RemoteGwIDs, remoteGwID)
	go func(user models.User, remoteGwID string) {
		extclients, err := logic.GetAllExtClients()
		if err != nil {
			slog.Error("failed to fetch extclients", "error", err)
			return
		}
		for _, extclient := range extclients {
			if extclient.OwnerID == user.UserName && remoteGwID == extclient.IngressGatewayID {
				logic.DeleteExtClient(extclient.Network, extclient.ClientID)
			}
		}
	}(*user, remoteGwID)

	err = logic.UpsertUser(*user)
	if err != nil {
		slog.Error("failed to update user gateways", "user", username, "error", err)
		logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("failed to fetch remote access gaetway node "+err.Error()), "badrequest"))
		return
	}
	json.NewEncoder(w).Encode(logic.ToReturnUser(*user))
}

// swagger:route GET "/api/users/{username}/remote_access_gw" nodes getUserRemoteAccessGws
//
// Get an individual node.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: nodeResponse
func getUserRemoteAccessGws(w http.ResponseWriter, r *http.Request) {
	// set header.
	w.Header().Set("Content-Type", "application/json")

	var params = mux.Vars(r)
	username := params["username"]
	if username == "" {
		logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("required params username"), "badrequest"))
		return
	}
	var req models.UserRemoteGwsReq
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		slog.Error("error decoding request body: ", "error", err)
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}
	if req.RemoteAccessClientID == "" {
		logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("remote access client id cannot be empty"), "badrequest"))
		return
	}
	userGws := make(map[string][]models.UserRemoteGws)
	user, err := logic.GetUser(username)
	if err != nil {
		logger.Log(0, username, "failed to fetch user: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("failed to fetch user %s, error: %v", username, err), "badrequest"))
		return
	}
	if user.IsAdmin || user.IsSuperAdmin {
		logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("admins can visit dashboard to create remote clients"), "badrequest"))
		return
	}
	allextClients, err := logic.GetAllExtClients()
	if err != nil {
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	}

	for _, extClient := range allextClients {
		if extClient.RemoteAccessClientID == req.RemoteAccessClientID && extClient.OwnerID == username {
			node, err := logic.GetNodeByID(extClient.IngressGatewayID)
			if err != nil {
				continue
			}
			if node.PendingDelete {
				continue
			}
			host, err := logic.GetHost(node.HostID.String())
			if err != nil {
				continue
			}

			if _, ok := user.RemoteGwIDs[node.ID.String()]; ok {
				gws := userGws[node.Network]

				gws = append(gws, models.UserRemoteGws{
					GwID:      node.ID.String(),
					GWName:    host.Name,
					Network:   node.Network,
					GwClient:  extClient,
					Connected: true,
				})
				userGws[node.Network] = gws
				delete(user.RemoteGwIDs, node.ID.String())

			}
		}

	}

	// add remaining gw nodes to resp
	for gwID := range user.RemoteGwIDs {
		node, err := logic.GetNodeByID(gwID)
		if err != nil {
			continue
		}
		if node.PendingDelete {
			continue
		}
		host, err := logic.GetHost(node.HostID.String())
		if err != nil {
			continue
		}
		gws := userGws[node.Network]

		gws = append(gws, models.UserRemoteGws{
			GwID:    node.ID.String(),
			GWName:  host.Name,
			Network: node.Network,
		})
		userGws[node.Network] = gws
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(userGws)
}

// swagger:route GET /api/users user getUsers
//
// Get all users.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: userBodyResponse
func getUsers(w http.ResponseWriter, r *http.Request) {
	// set header.
	w.Header().Set("Content-Type", "application/json")

	users, err := logic.GetUsers()

	if err != nil {
		logger.Log(0, "failed to fetch users: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	}

	logic.SortUsers(users[:])
	logger.Log(2, r.Header.Get("user"), "fetched users")
	json.NewEncoder(w).Encode(users)
}

// swagger:route POST /api/users/adm/createsuperadmin user createAdmin
//
// Make a user an admin.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: userBodyResponse
func createSuperAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	var u models.User

	err := json.NewDecoder(r.Body).Decode(&u)
	if err != nil {

		logger.Log(0, u.UserName, "error decoding request body: ",
			err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}

	if !servercfg.IsBasicAuthEnabled() {
		logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("basic auth is disabled"), "badrequest"))
		return
	}

	err = logic.CreateSuperAdmin(&u)
	if err != nil {
		logger.Log(0, u.UserName, "failed to create admin: ",
			err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}

	logger.Log(1, u.UserName, "was made a super admin")
	json.NewEncoder(w).Encode(logic.ToReturnUser(u))
}

// swagger:route POST /api/users/adm/transfersuperadmin user transferSuperAdmin
//
// Transfers suoeradmin role to an admin user.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: userBodyResponse
func transferSuperAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	caller, err := logic.GetUser(r.Header.Get("user"))
	if err != nil {
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
	}
	if !caller.IsSuperAdmin {
		logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("only superadmin can assign the superadmin role to another user"), "forbidden"))
		return
	}
	var params = mux.Vars(r)
	username := params["username"]
	u, err := logic.GetUser(username)
	if err != nil {
		slog.Error("error getting user", "user", u.UserName, "error", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}
	if !u.IsAdmin {
		logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("only admins can be promoted to superadmin role"), "forbidden"))
		return
	}
	if !servercfg.IsBasicAuthEnabled() {
		logic.ReturnErrorResponse(w, r, logic.FormatError(fmt.Errorf("basic auth is disabled"), "badrequest"))
		return
	}

	u.IsSuperAdmin = true
	u.IsAdmin = false
	err = logic.UpsertUser(*u)
	if err != nil {
		slog.Error("error updating user to superadmin: ", "user", u.UserName, "error", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	}
	caller.IsSuperAdmin = false
	caller.IsAdmin = true
	err = logic.UpsertUser(*caller)
	if err != nil {
		slog.Error("error demoting user to admin: ", "user", caller.UserName, "error", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	}
	slog.Info("user was made a super admin", "user", u.UserName)
	json.NewEncoder(w).Encode(logic.ToReturnUser(*u))
}

// swagger:route POST /api/users/{username} user createUser
//
// Create a user.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: userBodyResponse
func createUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	caller, err := logic.GetUser(r.Header.Get("user"))
	if err != nil {
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
	}
	var user models.User
	err = json.NewDecoder(r.Body).Decode(&user)
	if err != nil {
		logger.Log(0, user.UserName, "error decoding request body: ",
			err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}
	if !caller.IsSuperAdmin && user.IsAdmin {
		err = errors.New("only superadmin can create admin users")
		slog.Error("error creating new user: ", "user", user.UserName, "error", err)
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "forbidden"))
		return
	}
	if user.IsSuperAdmin {
		err = errors.New("additional superadmins cannot be created")
		slog.Error("error creating new user: ", "user", user.UserName, "error", err)
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "forbidden"))
		return
	}

	err = logic.CreateUser(&user)
	if err != nil {
		logger.Log(0, user.UserName, "error creating new user: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}
	logger.Log(1, user.UserName, "was created")
	json.NewEncoder(w).Encode(logic.ToReturnUser(user))
}

// swagger:route PUT /api/users/{username} user updateUser
//
// Update a user.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: userBodyResponse
func updateUser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var params = mux.Vars(r)
	// start here

	caller, err := logic.GetUser(r.Header.Get("user"))
	if err != nil {
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
	}
	username := params["username"]
	user, err := logic.GetUser(username)
	if err != nil {
		logger.Log(0, username,
			"failed to update user info: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	}
	var userchange models.User
	// we decode our body request params
	err = json.NewDecoder(r.Body).Decode(&userchange)
	if err != nil {
		logger.Log(0, username, "error decoding request body: ",
			err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}
	selfUpdate := false
	if caller.UserName == user.UserName {
		selfUpdate = true
	}

	if !caller.IsSuperAdmin {
		if user.IsSuperAdmin {
			slog.Error("non-superadmin user", "caller", caller.UserName, "attempted to update superadmin user", username)
			logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("cannot update superadmin user"), "forbidden"))
			return
		}
		if !selfUpdate && !(caller.IsAdmin) {
			slog.Error("non-admin user", "caller", caller.UserName, "attempted to update  user", username)
			logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("not authorized"), "forbidden"))
			return
		}

		if userchange.IsAdmin != user.IsAdmin || userchange.IsSuperAdmin != user.IsSuperAdmin {
			if selfUpdate {
				slog.Error("user cannot change his own role", "caller", caller.UserName, "attempted to update user role", username)
				logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("user not allowed to self assign role"), "forbidden"))
				return
			}
		}
		if !selfUpdate && caller.IsAdmin && userchange.IsAdmin {
			slog.Error("admin user cannot update another admin", "caller", caller.UserName, "attempted to update admin user", username)
			logic.ReturnErrorResponse(w, r, logic.FormatError(errors.New("admin user cannot update another admin"), "forbidden"))
			return
		}
	}

	if auth.IsOauthUser(user) == nil {
		err := fmt.Errorf("cannot update user info for oauth user %s", username)
		logger.Log(0, err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "forbidden"))
		return
	}

	user, err = logic.UpdateUser(&userchange, user)
	if err != nil {
		logger.Log(0, username,
			"failed to update user info: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}
	logger.Log(1, username, "was updated")
	json.NewEncoder(w).Encode(logic.ToReturnUser(*user))
}

// swagger:route DELETE /api/users/{username} user deleteUser
//
// Delete a user.
//
//			Schemes: https
//
//			Security:
//	  		oauth
//
//			Responses:
//				200: userBodyResponse
func deleteUser(w http.ResponseWriter, r *http.Request) {
	// Set header
	w.Header().Set("Content-Type", "application/json")

	// get params
	var params = mux.Vars(r)
	caller, err := logic.GetUser(r.Header.Get("user"))
	if err != nil {
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
	}
	username := params["username"]
	user, err := logic.GetUser(username)
	if err != nil {
		logger.Log(0, username,
			"failed to update user info: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	}
	if user.IsSuperAdmin {
		slog.Error(
			"failed to delete user: ", "user", username, "error", "superadmin cannot be deleted")
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	}
	if !caller.IsSuperAdmin {
		if caller.IsAdmin && user.IsAdmin {
			slog.Error(
				"failed to delete user: ", "user", username, "error", "admin cannot delete another admin user")
			logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
			return
		}
	}
	success, err := logic.DeleteUser(username)
	if err != nil {
		logger.Log(0, username,
			"failed to delete user: ", err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "internal"))
		return
	} else if !success {
		err := errors.New("delete unsuccessful")
		logger.Log(0, username, err.Error())
		logic.ReturnErrorResponse(w, r, logic.FormatError(err, "badrequest"))
		return
	}

	logger.Log(1, username, "was deleted")
	json.NewEncoder(w).Encode(params["username"] + " deleted.")
}

// Called when vpn client dials in to start the auth flow and first stage is to get register URL itself
func socketHandler(w http.ResponseWriter, r *http.Request) {
	// Upgrade our raw HTTP connection to a websocket based one
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Log(0, "error during connection upgrade for node sign-in:", err.Error())
		return
	}
	if conn == nil {
		logger.Log(0, "failed to establish web-socket connection during node sign-in")
		return
	}
	// Start handling the session
	go auth.SessionHandler(conn)
}
