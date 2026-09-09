package role

// Role is the nominal type for Izen's core operational role names.
// The untyped constants RoleDefault/RolePlan/... remain assignable to Role,
// and ValidRoles/IsValidRole continue to operate on plain strings so all
// existing callers keep working unchanged.
type Role string

// Core operational roles as typed Role values.
const (
	RoleDefaultT Role = RoleDefault
	RolePlanT    Role = RolePlan
	RoleSmolT    Role = RoleSmol
	RoleVisionT  Role = RoleVision
	RoleAdviserT Role = RoleAdviser
)
