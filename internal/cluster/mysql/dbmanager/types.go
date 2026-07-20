package dbmanager

type CreateUserRequest struct {
	JobID        string `json:"job_id"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	Superuser    bool   `json:"superuser"`
	SSLRequired  *bool  `json:"ssl_required"`
	DatabaseName string `json:"database,omitempty"`
}

func (r CreateUserRequest) SSLRequiredEnabled() bool {
	if r.SSLRequired == nil {
		return true
	}
	return *r.SSLRequired
}

type ResetPasswordRequest struct {
	JobID    string `json:"job_id"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type UpdateUserRequest struct {
	JobID       string `json:"job_id"`
	Username    string `json:"username"`
	NewUsername string `json:"new_username"`
}

type DeleteUserRequest struct {
	JobID    string `json:"job_id"`
	Username string `json:"username"`
}

type CreateDatabaseRequest struct {
	JobID  string `json:"job_id"`
	DBName string `json:"dbname"`
}

type UpdateDatabaseRequest struct {
	JobID     string `json:"job_id"`
	DBName    string `json:"dbname"`
	NewDBName string `json:"new_dbname"`
}

type DeleteDatabaseRequest struct {
	JobID  string `json:"job_id"`
	DBName string `json:"dbname"`
}

type SetConnectionLimitRequest struct {
	JobID           string `json:"job_id"`
	ConnectionLimit int    `json:"connection_limit"`
}

// NodeConnectionLimit is the per-node outcome of a connection-limit read or
// edit: the live max_connections value, or the error that prevented reading
// or applying it.
type NodeConnectionLimit struct {
	IP             string `json:"ip"`
	Role           string `json:"role"`
	MaxConnections int    `json:"max_connections,omitempty"`
	Error          string `json:"error,omitempty"`
}

// ConnectionLimitStatus reports the configured connection limit from the
// stored job spec (0 = engine default) alongside the live per-node values.
type ConnectionLimitStatus struct {
	JobID           string                `json:"job_id"`
	ConnectionLimit int                   `json:"connection_limit"`
	Nodes           []NodeConnectionLimit `json:"nodes"`
}
