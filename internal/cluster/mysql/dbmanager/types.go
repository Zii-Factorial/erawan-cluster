package dbmanager

// CreateUserRequest creates (or updates) a MySQL user with full DML+DDL
// privileges on every database.  If the user already exists, the password is
// refreshed and grants re-applied (idempotent).
type CreateUserRequest struct {
	PrimaryIP     string `json:"primary_ip"`
	Port          int    `json:"port"`
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	Username      string `json:"username"`
	Password      string `json:"password"`
}

// DeleteUserRequest drops a MySQL user.  System users and users with the
// SUPER privilege are refused.
type DeleteUserRequest struct {
	PrimaryIP     string `json:"primary_ip"`
	Port          int    `json:"port"`
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	Username      string `json:"username"`
}

// CreateDatabaseRequest creates a database and grants every existing
// non-system user full access to it.
type CreateDatabaseRequest struct {
	PrimaryIP     string `json:"primary_ip"`
	Port          int    `json:"port"`
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	DBName        string `json:"dbname"`
}

// DeleteDatabaseRequest kills all connections to the database and drops it.
// System databases are refused.
type DeleteDatabaseRequest struct {
	PrimaryIP     string `json:"primary_ip"`
	Port          int    `json:"port"`
	AdminUser     string `json:"admin_user"`
	AdminPassword string `json:"admin_password"`
	DBName        string `json:"dbname"`
}
