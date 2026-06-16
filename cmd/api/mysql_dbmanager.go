package main

import (
	"net/http"

	mysqldbmanager "erawan-cluster/internal/cluster/mysql/dbmanager"
)

func (app *application) createMySQLUserHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.CreateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.mysqlDB.CreateUser(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "user created", nil)
}

func (app *application) resetMySQLPasswordHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.ResetPasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.mysqlDB.ResetPassword(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "password reset", nil)
}

func (app *application) updateMySQLUserHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.UpdateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.mysqlDB.UpdateUser(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "user renamed", nil)
}

func (app *application) deleteMySQLUserHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.DeleteUserRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.mysqlDB.DeleteUser(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "user deleted", nil)
}

func (app *application) createMySQLDatabaseHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.CreateDatabaseRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.mysqlDB.CreateDatabase(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "database created", nil)
}

func (app *application) updateMySQLDatabaseHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.UpdateDatabaseRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.mysqlDB.UpdateDatabase(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "database renamed", nil)
}

func (app *application) deleteMySQLDatabaseHandler(w http.ResponseWriter, r *http.Request) {
	var req mysqldbmanager.DeleteDatabaseRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.mysqlDB.DeleteDatabase(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "database deleted", nil)
}
