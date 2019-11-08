package a

import (
	"database/sql"
	"fmt"
)

func RowsErrCheck() {
	rows, _ := db.Query("")
	for rows.Next() {

	}
	_ = rows.Err() // OK
}

func f2() {
	rows, err := db.Query("") // OK
	if err != nil {
		// handle error
	}
	rowsS := rows
	_ = rowsS.Err()

	rows2, err := db.Query("") // OK
	rowsX2 := rows2
	_ = rowsX2.Err()
	if err != nil {
		// handle error
	}
}

func f4() {
	rows, err := db.Query("") // want "rows err must be checked"
	if err != nil {
		// handle error
	}
	fmt.Print(rows.NextResultSet())

	rows, err = db.Query("") // want "rows err must be checked"
	if err != nil {
		// handle error
	}
	fmt.Print(rows.NextResultSet())

	rows, err = db.Query("") // want "rows err must be checked"
	if err != nil {
		// handle error
	}
	fmt.Print(rows.NextResultSet())
	return
}

func f5() {
	_, err := db.Query("") // want "rows err must be checked"
	if err != nil {
		// handle error
	}
}

func f6() {
	db.Query("") // want "rows err must be checked"
}

func f7() {
	rows, _ := db.Query("") // OK
	resCloser := func() error {
		return rows.Err()
	}
	_ = resCloser()
}

func f8() {
	rows, _ := db.Query("") // want "rows err must be checked"
	_ = func() {
		rows.Close()
	}
}

func f9() {
	_ = func() {
		rows, _ := db.Query("") // OK
		rows.Err()
	}
}

func f10() {
	rows, _ := db.Query("")
	resCloser := func(rs *sql.Rows) {
		_ = rs.Err()
	}
	resCloser(rows)
}
