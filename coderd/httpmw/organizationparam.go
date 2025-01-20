package httpmw

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/coder/coder/v2/coderd/database"
	"github.com/coder/coder/v2/coderd/database/dbauthz"
	"github.com/coder/coder/v2/coderd/httpapi"
	"github.com/coder/coder/v2/codersdk"
)

type (
	organizationParamContextKey       struct{}
	organizationMemberParamContextKey struct{}
)

// OrganizationParam returns the organization from the ExtractOrganizationParam handler.
func OrganizationParam(r *http.Request) database.Organization {
	organization, ok := r.Context().Value(organizationParamContextKey{}).(database.Organization)
	if !ok {
		panic("developer error: organization param middleware not provided")
	}
	return organization
}

// OrganizationMemberParam returns the organization membership that allowed the query
// from the ExtractOrganizationParam handler.
func OrganizationMemberParam(r *http.Request) OrganizationMember {
	organizationMember, ok := r.Context().Value(organizationMemberParamContextKey{}).(OrganizationMember)
	if !ok {
		panic("developer error: organization member param middleware not provided")
	}
	return organizationMember
}

// ExtractOrganizationParam grabs an organization from the "organization" URL parameter.
// This middleware requires the API key middleware higher in the call stack for authentication.
func ExtractOrganizationParam(db database.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			arg := chi.URLParam(r, "organization")
			if arg == "" {
				httpapi.Write(ctx, rw, http.StatusBadRequest, codersdk.Response{
					Message: "\"organization\" must be provided.",
				})
				return
			}

			var organization database.Organization
			var dbErr error

			// If the name is exactly "default", then we fetch the default
			// organization. This is a special case to make it easier
			// for single org deployments.
			//
			// arg == uuid.Nil.String() should be a temporary workaround for
			// legacy provisioners that don't provide an organization ID.
			// This prevents a breaking change.
			// TODO: This change was added March 2024. Nil uuid returning the
			// 		default org should be removed some number of months after
			//		that date.
			if arg == codersdk.DefaultOrganization || arg == uuid.Nil.String() {
				organization, dbErr = db.GetDefaultOrganization(ctx)
			} else {
				// Try by name or uuid.
				id, err := uuid.Parse(arg)
				if err == nil {
					organization, dbErr = db.GetOrganizationByID(ctx, id)
				} else {
					organization, dbErr = db.GetOrganizationByName(ctx, arg)
				}
			}
			if httpapi.Is404Error(dbErr) {
				httpapi.ResourceNotFound(rw)
				return
			}
			if dbErr != nil {
				httpapi.Write(ctx, rw, http.StatusInternalServerError, codersdk.Response{
					Message: fmt.Sprintf("Internal error fetching organization %q.", arg),
					Detail:  dbErr.Error(),
				})
				return
			}
			ctx = context.WithValue(ctx, organizationParamContextKey{}, organization)
			next.ServeHTTP(rw, r.WithContext(ctx))
		})
	}
}

// OrganizationMember is the database object plus the Username and Avatar URL. Including these
// in the middleware is preferable to a join at the SQL layer so that we can keep the
// autogenerated database types as they are.
type OrganizationMember struct {
	database.OrganizationMember
	Username  string
	AvatarURL string
}

// ExtractOrganizationMemberParam grabs a user membership from the "organization" and "user" URL parameter.
// This middleware requires the ExtractUser and ExtractOrganization middleware higher in the stack
func ExtractOrganizationMemberParam(db database.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			// We need to resolve the `{user}` URL parameter so that we can get the userID and
			// username.  We do this as SystemRestricted since the caller might have permission
			// to access the OrganizationMember object, but *not* the User object.  So, it is
			// very important that we do not add the User object to the request context or otherwise
			// leak it to the API handler.
			// nolint:gocritic
			user, ok := extractUserContext(dbauthz.AsSystemRestricted(ctx), db, rw, r)
			if !ok {
				return
			}
			organization := OrganizationParam(r)

			organizationMember, err := database.ExpectOne(db.OrganizationMembers(ctx, database.OrganizationMembersParams{
				OrganizationID: organization.ID,
				UserID:         user.ID,
			}))
			if httpapi.Is404Error(err) {
				httpapi.ResourceNotFound(rw)
				return
			}
			if err != nil {
				httpapi.Write(ctx, rw, http.StatusInternalServerError, codersdk.Response{
					Message: "Internal error fetching organization member.",
					Detail:  err.Error(),
				})
				return
			}

			ctx = context.WithValue(ctx, organizationMemberParamContextKey{}, OrganizationMember{
				OrganizationMember: organizationMember.OrganizationMember,
				// Here we're making two exceptions to the rule about not leaking data about the user
				// to the API handler, which is to include the username and avatar URL.
				// If the caller has permission to read the OrganizationMember, then we're explicitly
				// saying here that they also have permission to see the member's username and avatar.
				// This is OK!
				//
				// API handlers need this information for audit logging and returning the owner's
				// username in response to creating a workspace. Additionally, the frontend consumes
				// the Avatar URL and this allows the FE to avoid an extra request.
				Username:  user.Username,
				AvatarURL: user.AvatarURL,
			})
			next.ServeHTTP(rw, r.WithContext(ctx))
		})
	}
}
