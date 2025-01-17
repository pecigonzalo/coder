package coderd

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"golang.org/x/xerrors"

	"github.com/coder/coder/coderd"
	"github.com/coder/coder/coderd/database"
	"github.com/coder/coder/coderd/httpapi"
	"github.com/coder/coder/coderd/httpmw"
	"github.com/coder/coder/coderd/rbac"
	"github.com/coder/coder/codersdk"
)

func (api *API) postGroupByOrganization(rw http.ResponseWriter, r *http.Request) {
	var (
		ctx = r.Context()
		org = httpmw.OrganizationParam(r)
	)

	if !api.Authorize(r, rbac.ActionCreate, rbac.ResourceGroup) {
		http.NotFound(rw, r)
		return
	}

	var req codersdk.CreateGroupRequest
	if !httpapi.Read(ctx, rw, r, &req) {
		return
	}

	if req.Name == database.AllUsersGroup {
		httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
			Message: fmt.Sprintf("%q is a reserved keyword and cannot be used for a group name.", database.AllUsersGroup),
		})
		return
	}

	group, err := api.Database.InsertGroup(ctx, database.InsertGroupParams{
		ID:             uuid.New(),
		Name:           req.Name,
		OrganizationID: org.ID,
	})
	if database.IsUniqueViolation(err) {
		httpapi.Write(ctx, rw, http.StatusConflict, codersdk.Response{
			Message: fmt.Sprintf("Group with name %q already exists.", req.Name),
		})
		return
	}
	if err != nil {
		httpapi.InternalServerError(rw, err)
		return
	}

	httpapi.Write(ctx, rw, http.StatusCreated, convertGroup(group, nil))
}

func (api *API) patchGroup(rw http.ResponseWriter, r *http.Request) {
	var (
		ctx   = r.Context()
		group = httpmw.GroupParam(r)
	)

	if !api.Authorize(r, rbac.ActionUpdate, group) {
		http.NotFound(rw, r)
		return
	}

	var req codersdk.PatchGroupRequest
	if !httpapi.Read(ctx, rw, r, &req) {
		return
	}

	if req.Name != "" && req.Name == database.AllUsersGroup {
		httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
			Message: fmt.Sprintf("%q is a reserved group name!", database.AllUsersGroup),
		})
		return
	}

	users := make([]string, 0, len(req.AddUsers)+len(req.RemoveUsers))
	users = append(users, req.AddUsers...)
	users = append(users, req.RemoveUsers...)

	for _, id := range users {
		if _, err := uuid.Parse(id); err != nil {
			httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
				Message: fmt.Sprintf("ID %q must be a valid user UUID.", id),
			})
			return
		}
		// TODO: It would be nice to enforce this at the schema level
		// but unfortunately our org_members table does not have an ID.
		_, err := api.Database.GetOrganizationMemberByUserID(ctx, database.GetOrganizationMemberByUserIDParams{
			OrganizationID: group.OrganizationID,
			UserID:         uuid.MustParse(id),
		})
		if xerrors.Is(err, sql.ErrNoRows) {
			httpapi.Write(ctx, rw, http.StatusPreconditionFailed, codersdk.Response{
				Message: fmt.Sprintf("User %q must be a member of organization %q", id, group.ID),
			})
			return
		}
		if err != nil {
			httpapi.InternalServerError(rw, err)
			return
		}
	}
	if req.Name != "" {
		_, err := api.Database.GetGroupByOrgAndName(ctx, database.GetGroupByOrgAndNameParams{
			OrganizationID: group.OrganizationID,
			Name:           req.Name,
		})
		if err == nil {
			httpapi.Write(ctx, rw, http.StatusConflict, codersdk.Response{
				Message: fmt.Sprintf("A group with name %q already exists.", req.Name),
			})
			return
		}
	}

	err := api.Database.InTx(func(tx database.Store) error {
		if req.Name != "" {
			var err error
			group, err = tx.UpdateGroupByID(ctx, database.UpdateGroupByIDParams{
				ID:   group.ID,
				Name: req.Name,
			})
			if err != nil {
				return xerrors.Errorf("update group by ID: %w", err)
			}
		}
		for _, id := range req.AddUsers {
			err := tx.InsertGroupMember(ctx, database.InsertGroupMemberParams{
				GroupID: group.ID,
				UserID:  uuid.MustParse(id),
			})
			if err != nil {
				return xerrors.Errorf("insert group member %q: %w", id, err)
			}
		}
		for _, id := range req.RemoveUsers {
			err := tx.DeleteGroupMember(ctx, uuid.MustParse(id))
			if err != nil {
				return xerrors.Errorf("insert group member %q: %w", id, err)
			}
		}
		return nil
	})
	if database.IsUniqueViolation(err) {
		httpapi.Write(ctx, rw, http.StatusPreconditionFailed, codersdk.Response{
			Message: "Cannot add the same user to a group twice!",
			Detail:  err.Error(),
		})
		return
	}
	if xerrors.Is(err, sql.ErrNoRows) {
		httpapi.Write(ctx, rw, http.StatusPreconditionFailed, codersdk.Response{
			Message: "Failed to add or remove non-existent group member",
			Detail:  err.Error(),
		})
		return
	}
	if err != nil {
		httpapi.InternalServerError(rw, err)
		return
	}

	members, err := api.Database.GetGroupMembers(ctx, group.ID)
	if err != nil {
		httpapi.InternalServerError(rw, err)
		return
	}

	httpapi.Write(ctx, rw, http.StatusOK, convertGroup(group, members))
}

func (api *API) deleteGroup(rw http.ResponseWriter, r *http.Request) {
	var (
		ctx   = r.Context()
		group = httpmw.GroupParam(r)
	)

	if !api.Authorize(r, rbac.ActionDelete, group) {
		httpapi.ResourceNotFound(rw)
		return
	}

	if group.Name == database.AllUsersGroup {
		httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
			Message: fmt.Sprintf("%q is a reserved group and cannot be deleted!", database.AllUsersGroup),
		})
		return
	}

	err := api.Database.DeleteGroupByID(ctx, group.ID)
	if err != nil {
		httpapi.InternalServerError(rw, err)
		return
	}

	httpapi.Write(ctx, rw, http.StatusOK, codersdk.Response{
		Message: "Successfully deleted group!",
	})
}

func (api *API) group(rw http.ResponseWriter, r *http.Request) {
	var (
		ctx   = r.Context()
		group = httpmw.GroupParam(r)
	)

	if !api.Authorize(r, rbac.ActionRead, group) {
		httpapi.ResourceNotFound(rw)
		return
	}

	users, err := api.Database.GetGroupMembers(ctx, group.ID)
	if err != nil && !xerrors.Is(err, sql.ErrNoRows) {
		httpapi.InternalServerError(rw, err)
		return
	}

	httpapi.Write(ctx, rw, http.StatusOK, convertGroup(group, users))
}

func (api *API) groups(rw http.ResponseWriter, r *http.Request) {
	var (
		ctx = r.Context()
		org = httpmw.OrganizationParam(r)
	)

	groups, err := api.Database.GetGroupsByOrganizationID(ctx, org.ID)
	if err != nil && !xerrors.Is(err, sql.ErrNoRows) {
		httpapi.InternalServerError(rw, err)
		return
	}

	// Filter groups based on rbac permissions
	groups, err = coderd.AuthorizeFilter(api.AGPL.HTTPAuth, r, rbac.ActionRead, groups)
	if err != nil {
		httpapi.Write(ctx, rw, http.StatusInternalServerError, codersdk.Response{
			Message: "Internal error fetching groups.",
			Detail:  err.Error(),
		})
		return
	}

	resp := make([]codersdk.Group, 0, len(groups))
	for _, group := range groups {
		members, err := api.Database.GetGroupMembers(ctx, group.ID)
		if err != nil {
			httpapi.InternalServerError(rw, err)
			return
		}

		resp = append(resp, convertGroup(group, members))
	}

	httpapi.Write(ctx, rw, http.StatusOK, resp)
}

func convertGroup(g database.Group, users []database.User) codersdk.Group {
	// It's ridiculous to query all the orgs of a user here
	// especially since as of the writing of this comment there
	// is only one org. So we pretend everyone is only part of
	// the group's organization.
	orgs := make(map[uuid.UUID][]uuid.UUID)
	for _, user := range users {
		orgs[user.ID] = []uuid.UUID{g.OrganizationID}
	}
	return codersdk.Group{
		ID:             g.ID,
		Name:           g.Name,
		OrganizationID: g.OrganizationID,
		Members:        convertUsers(users, orgs),
	}
}

func convertUser(user database.User, organizationIDs []uuid.UUID) codersdk.User {
	convertedUser := codersdk.User{
		ID:              user.ID,
		Email:           user.Email,
		CreatedAt:       user.CreatedAt,
		LastSeenAt:      user.LastSeenAt,
		Username:        user.Username,
		Status:          codersdk.UserStatus(user.Status),
		OrganizationIDs: organizationIDs,
		Roles:           make([]codersdk.Role, 0, len(user.RBACRoles)),
		AvatarURL:       user.AvatarURL.String,
	}

	for _, roleName := range user.RBACRoles {
		rbacRole, _ := rbac.RoleByName(roleName)
		convertedUser.Roles = append(convertedUser.Roles, convertRole(rbacRole))
	}

	return convertedUser
}

func convertUsers(users []database.User, organizationIDsByUserID map[uuid.UUID][]uuid.UUID) []codersdk.User {
	converted := make([]codersdk.User, 0, len(users))
	for _, u := range users {
		userOrganizationIDs := organizationIDsByUserID[u.ID]
		converted = append(converted, convertUser(u, userOrganizationIDs))
	}
	return converted
}

func convertRole(role rbac.Role) codersdk.Role {
	return codersdk.Role{
		DisplayName: role.DisplayName,
		Name:        role.Name,
	}
}
