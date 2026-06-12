package dbmanager

type CreateUserRequest struct {
	JobID    string `json:"job_id"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type DeleteUserRequest struct {
	JobID    string `json:"job_id"`
	Username string `json:"username"`
}

type CreateDatabaseRequest struct {
	JobID  string `json:"job_id"`
	DBName string `json:"dbname"`
}

type DeleteDatabaseRequest struct {
	JobID  string `json:"job_id"`
	DBName string `json:"dbname"`
}
