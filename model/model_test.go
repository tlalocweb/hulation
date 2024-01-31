package model

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/assert"
	"github.com/tlalocweb/hulation/config"
	"github.com/tlalocweb/hulation/log"
	"gorm.io/gorm"
)

var testdb *gorm.DB
var testsql *sql.DB

// Visitor model
func TestVisitorInsertDelete(t *testing.T) {
	// Create new visitor
	visitor := Visitor{
		Email:     "test@test.com",
		FirstName: "Test",
		LastName:  "Test",
	}
	// Create new visitor
	result := testdb.Create(&visitor)

	if result.Error != nil {
		t.Errorf("Error creating visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Insert ok. ID of new visitor is: ", visitor.ID)
	}

	result = testdb.Delete(&visitor)
	//.Where("id = ?", visitor.ID)

	if result.Error != nil {
		t.Errorf("Error deleting visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Delete ok.")
	}
}

func TestVisitorAddCookieToVisitor(t *testing.T) {
	// Create new visitor
	visitor := Visitor{
		Email:     "test2@test.com",
		FirstName: "Test",
		LastName:  "Test",
	}
	// Create new visitor
	result := testdb.Create(&visitor)

	if result.Error != nil {
		t.Errorf("Error creating visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Insert ok. ID of new visitor is: ", visitor.ID)
	}

	cookie, err := visitor.NewVisitorCookie()
	if err != nil {
		t.Errorf("Error creating cookie: %v", err)
		t.Error(err)
	}

	err = cookie.Commit(testdb)
	if err != nil {
		t.Errorf("Error committing cookie: %v", err)
	}

	// err = AddCookieToVisitor(testdb, &visitor, cookie)
	// if err != nil {
	// 	t.Errorf("Error adding cookie to visitor: %v", err)
	// 	t.Error(err)
	// }

	// lookup the vistor by the cookie

	var visitorlookup *Visitor

	visitorlookup, err = GetVisitorByCookie(testdb, cookie.Cookie)

	if err != nil {
		t.Errorf("Error looking up visitor by cookie: %v", err)
		t.Error(err)
	} else {
		assert.NotNil(t, visitorlookup)
		if visitorlookup != nil {
			assert.Equal(t, visitor.ID, visitorlookup.ID)
		}
	}

	cookiem, err := CookieFromCookieVal(testdb, cookie.Cookie, visitorlookup)

	if err != nil {
		t.Errorf("Error looking up visitor by sscookie: %v", err)
		t.Error(err)
	} else {
		assert.NotNil(t, cookiem)
		if cookiem != nil {
			assert.Equal(t, visitorlookup.ID, cookiem.BelongsTo)
		}
	}

	// test CookieFromCookieVal again - but with a missing cookie
	cookiem2, err := CookieFromCookieVal(testdb, "blah", visitorlookup)
	if err != nil {
		t.Errorf("Error looking up visitor by sscookie: %v", err)
		t.Error(err)
	} else {
		assert.NotNil(t, cookiem2)
		if cookiem2 != nil {
			assert.Equal(t, visitorlookup.ID, cookiem2.BelongsTo)
		}
		assert.NotEqual(t, cookiem2.Cookie, cookiem.Cookie)
	}

	result = testdb.Delete(&visitor)

	if result.Error != nil {
		t.Errorf("Error deleting visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Delete ok.")
	}
}

func TestVisitorAddSSCookieToVisitor(t *testing.T) {
	// Create new visitor
	visitor := Visitor{
		Email:     "testsscookie@test.com",
		FirstName: "Test",
		LastName:  "Test",
	}
	// Create new visitor
	result := testdb.Create(&visitor)

	if result.Error != nil {
		t.Errorf("Error creating visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Insert ok. ID of new visitor is: ", visitor.ID)
	}

	sscookie, err := visitor.NewVisitorSSCookie()
	if err != nil {
		t.Errorf("Error creating cookie: %v", err)
		t.Error(err)
	}

	err = sscookie.Commit(testdb)
	if err != nil {
		t.Errorf("Error committing cookie: %v", err)
	}

	// wait a moment for the commit to finish - the call above does not guarantee a lookup
	// immediately following will work.
	// t.Logf("Planned 1 sec wait for commit to finish.")
	// time.Sleep(1 * time.Second)
	// lookup the vistor by the sscookie
	var visitorlookup *Visitor

	visitorlookup, err = GetVisitorBySSCookie(testdb, sscookie.Cookie)

	if err != nil {
		t.Errorf("Error looking up visitor by sscookie: %v", err)
		t.Error(err)
	} else {
		assert.NotNil(t, visitorlookup)
		if visitorlookup != nil {
			assert.Equal(t, visitor.ID, visitorlookup.ID)
		}
	}

	cookiem, err := SSCookieFromSSCookieVal(testdb, sscookie.Cookie, visitorlookup)

	if err != nil {
		t.Errorf("Error looking up visitor by sscookie: %v", err)
		t.Error(err)
	} else {
		assert.NotNil(t, cookiem)
		if cookiem != nil {
			assert.Equal(t, visitorlookup.ID, cookiem.BelongsTo)
		}
	}

	result = testdb.Delete(&visitor)

	if result.Error != nil {
		t.Errorf("Error deleting visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Delete ok.")
	}
}

func TestVisitorAddAlias(t *testing.T) {
	// Create new visitor
	visitor := &Visitor{
		Email:     "test3@test.com",
		FirstName: "Test",
		LastName:  "Test3",
	}
	// Create new visitor
	result := testdb.Create(visitor)

	if result.Error != nil {
		t.Errorf("Error creating visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Insert ok. ID of new visitor is: ", visitor.ID)
	}

	alias := &Alias{
		Email:     "jimbo@google.com",
		FirstName: "Jimbo",
		LastName:  "Jones",
	}

	err := UpsertAliasForVisitor(testdb, visitor, alias)

	if err != nil {
		t.Errorf("Error adding alias to visitor: %v", err)
		t.Error(err)
	}

	result = testdb.Delete(&visitor)

	if result.Error != nil {
		t.Errorf("Error deleting visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Delete ok.")
	}

}

func TestVisitorAddEvent(t *testing.T) {
	visitor := &Visitor{
		Email:     "test4@test.com",
		FirstName: "Test",
		LastName:  "Test4",
	}

	// Create new visitor
	result := testdb.Create(visitor)

	if result.Error != nil {
		t.Errorf("Error creating visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Insert ok. ID of new visitor is: ", visitor.ID)
	}

	event := NewEvent(100)

	event.SetData("testdata")
	event.SetURL("http://www.test.com")

	err := event.CommitTo(testdb, visitor)

	if err != nil {
		t.Errorf("Error committing event: %v", err)
		t.Error(err)
	}

	// update email
	visitor.Email = "jimbo@hello.com"
	visitor.Commit(testdb)

	// check email
	var visitorlookup *Visitor
	visitorlookup, err = GetVisitorByEmail(testdb, "jimbo@hello.com")

	if err != nil {
		t.Errorf("Error looking up visitor by email: %v", err)
		t.Error(err)
	} else {
		assert.Equal(t, visitor.ID, visitorlookup.ID)
	}

	result = testdb.Delete(&visitor)

	if result.Error != nil {
		t.Errorf("Error deleting visitor: %v", result.Error)
		t.Error(result.Error)
	} else {
		t.Log("Delete ok.")
	}

}

func TestVisitorAddMethod2(t *testing.T) {
	visitor := NewVisitor()

	visitor.SetEmail("test2@test.com")
	err := visitor.Commit(testdb)
	if err != nil {
		t.Errorf("Error committing visitor: %v", err)
		t.Error(err)
	}

	v2, err := GetVisitorByID(testdb, visitor.ID)

	if err != nil {
		t.Errorf("Error getting visitor: %v", err)
		t.Error(err)
	}

	assert.Equal(t, visitor.Email, v2.Email)

}

func TestNoVisitorByThatID(t *testing.T) {
	v, err := GetVisitorByID(testdb, "blah")

	if err != nil {
		t.Errorf("Zero record should not be an error: %v", err)
	}

	assert.Nil(t, v)

}

func TestNoVisitorByThatCookie(t *testing.T) {
	v, err := GetVisitorByCookie(testdb, "blah")

	if err != nil {
		t.Errorf("Zero record should not be an error: %v", err)
	}

	assert.Nil(t, v)
}

func TestNoVisitorByThatSSCookie(t *testing.T) {
	v, err := GetVisitorBySSCookie(testdb, "blah")

	if err != nil {
		t.Errorf("Zero record should not be an error: %v", err)
	}

	assert.Nil(t, v)
}

func TestMain(m *testing.M) {
	var err error
	var conf *config.Config
	conf, err = config.LoadConfig("./testmodel.yaml")
	SetDebugDBLogging(2)
	if err != nil {
		log.Errorf("Error with config: %v", err)
		fmt.Printf("Error with config: %v", err)
		os.Exit(1)
	}

	testsql, testdb, _, err = SetupDB(conf, func(ctx context.Context, conn *sql.DB) (err error) {
		// create database if not exists
		_, err = conn.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", conf.DBConfig.DBName))
		return
	})
	if err != nil {
		fmt.Printf("Clickhouse needs to be running.\nError setting up database: %v", err)
		log.Errorf("Clickhouse needs to be running.\nError setting up database: %v", err)
		log.Errorf("Config was: %s", spew.Sdump(conf))
		os.Exit(1)
	}
	// call flag.Parse() here if TestMain uses flags
	exitval := m.Run()

	// Close the database
	CloseDB(testdb)
	os.Exit(exitval)
}
