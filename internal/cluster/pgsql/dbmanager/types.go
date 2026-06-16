package dbmanager

type CreateUserRequest struct {
	JobID        string `json:"job_id"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	DatabaseName string `json:"database,omitempty"`
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
