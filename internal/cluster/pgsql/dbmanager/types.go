package dbmanager

// CreateUserRequest creates a new PostgreSQL role with full DML+DDL privileges
// on every existing non-system database.  If the role already exists its
// password is updated and privileges are re-applied (idempotent).
type CreateUserRequest struct {
	PrimaryIP     string `json:"primary_ip"`
	Port          int    `json:"port"`
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	Username      string `json:"username"`
	Password      string `json:"password"`
}

// DeleteUserRequest drops a PostgreSQL role after revoking all its privileges
// from every database on the cluster.  System roles and superusers are refused.
type DeleteUserRequest struct {
	PrimaryIP     string `json:"primary_ip"`
	Port          int    `json:"port"`
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	Username      string `json:"username"`
}

// CreateDatabaseRequest creates a database and immediately grants every
// existing non-system user full access to it.
type CreateDatabaseRequest struct {
	PrimaryIP     string `json:"primary_ip"`
	Port          int    `json:"port"`
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	DBName        string `json:"dbname"`
	// Owner is an optional existing role that will own the database (and can
	// therefore DROP it).  Defaults to admin_user when empty.
	Owner string `json:"owner"`
}

// DeleteDatabaseRequest terminates all connections and drops the database.
// System databases (postgres, template0, template1) are refused.
type DeleteDatabaseRequest struct {
	PrimaryIP     string `json:"primary_ip"`
	Port          int    `json:"port"`
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	DBName        string `json:"dbname"`
}
