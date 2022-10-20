package models

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/layer5io/meshkit/database"
	"github.com/layer5io/meshkit/logger"
)

// sanitizeOrderInput takes in the "order by" query, a validColums
// string slice and returns a sanitized query
//
// it will allow to run order by query only on the columns that are present
// in the validColumns string slice, if any other column is requested in the
// query then it will be IGNORED and an empty query would be returned instead
//
// sanitizeOrderInput also expects the query to be no longer than two words, that is
// the query may look like "updated_at DESC" or "name ASC"
func sanitizeOrderInput(order string, validColumns []string) string {
	parsedOrderStr := strings.Split(order, " ")
	if len(parsedOrderStr) != 2 {
		return ""
	}

	inputCol := parsedOrderStr[0]
	typ := strings.ToLower(parsedOrderStr[1])
	for _, col := range validColumns {
		if col == inputCol {
			if typ == "desc" {
				return fmt.Sprintf("%s desc", col)
			}

			return fmt.Sprintf("%s asc", col)
		}
	}

	return ""
}

var (
	dbHandler database.Handler
	mx        sync.Mutex
)

func setNewDBInstance(user string, pass string, host string, port string) {
	mx.Lock()
	defer mx.Unlock()

	// Initialize Logger instance
	log, err := logger.New("meshery", logger.Options{
		Format: logger.SyslogLogFormat,
	})
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}
	for {
		dbHandler, err = database.New(database.Options{
			Engine:   database.POSTGRES,
			Username: user,
			Password: pass,
			Host:     host,
			Port:     port,
			Logger:   log,
		})
		if err != nil {
			log.Error(ErrConnectingDatabase(err))
			log.Info(fmt.Sprintf("retrying after %d", 10))
			time.Sleep(10 * time.Second)
			continue
		}
		break
	}

}

func GetNewDBInstance(user string, pass string, host string, port string) *database.Handler {
	setNewDBInstance(user, pass, host, port)
	return &dbHandler
}
