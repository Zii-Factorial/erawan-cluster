package main

import (
	"net/http"

	dbmanager "erawan-cluster/internal/cluster/pgsql/dbmanager"
)

func (app *application) createPGSQLUserHandler(w http.ResponseWriter, r *http.Request) {
	var req dbmanager.CreateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.pgsqlDB.CreateUser(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "user created", nil)
}

func (app *application) deletePGSQLUserHandler(w http.ResponseWriter, r *http.Request) {
	var req dbmanager.DeleteUserRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.pgsqlDB.DeleteUser(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "user deleted", nil)
}

func (app *application) createPGSQLDatabaseHandler(w http.ResponseWriter, r *http.Request) {
	var req dbmanager.CreateDatabaseRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.pgsqlDB.CreateDatabase(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "database created", nil)
}

func (app *application) deletePGSQLDatabaseHandler(w http.ResponseWriter, r *http.Request) {
	var req dbmanager.DeleteDatabaseRequest
	if err := decodeJSON(r, &req); err != nil {
		errJSON(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if err := app.pgsqlDB.DeleteDatabase(r.Context(), req); err != nil {
		errJSON(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ok(w, "database deleted", nil)
}
