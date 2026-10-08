// Package ppgorm instruments the go-gorm/gorm package (https://github.com/go-gorm/gorm).
//
// This package instruments the go-gorm/gorm calls.
// Use the Open as the gorm.Open.
//
//	g, err := ppgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
//
// Or register the callbacks on a *gorm.DB opened elsewhere:
//
//	g = ppgorm.Instrument(g)
//
// It is necessary to pass the context containing the pinpoint.Tracer to gorm.DB.
//
//	g = g.WithContext(pinpoint.NewContext(context.Background(), tracer))
//	g.Create(&Product{Code: "D42", Price: 100})
package ppgorm

import (
	"errors"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"gorm.io/gorm"
)

// Open returns a new *gorm.DB ready to instrument.
func Open(dialector gorm.Dialector, opts ...gorm.Option) (*gorm.DB, error) {
	db, err := gorm.Open(dialector, opts...)
	if err != nil {
		return db, err
	}
	return Instrument(db), nil
}

// beforeCreateCallback is the first callback registerCallbacks adds; its
// presence tells an already instrumented db apart.
const beforeCreateCallback = "pinpoint:before_create"

// Instrument registers the plugin's callbacks on a db that gorm.Open returned
// and returns it. Open calls it; a compile-time hook on gorm.Open, or an
// application that opened the db itself, calls it directly. It is idempotent:
// a db that already carries the callbacks is left alone, so Open on top of an
// instrumented db, or Open next to a compile-time hook, records every
// statement once.
func Instrument(db *gorm.DB) *gorm.DB {
	if db == nil || db.Callback().Create().Get(beforeCreateCallback) != nil {
		return db
	}
	registerCallbacks(db)
	return db
}

func registerCallbacks(db *gorm.DB) {
	//The name of callback(before/after) should be matched with default callback of GORM
	//https://github.com/go-gorm/gorm/blob/master/callbacks/callbacks.go

	create := db.Callback().Create()
	_ = create.Before("gorm:before_create").Register(beforeCreateCallback, wrapBefore("gorm.create"))
	_ = create.After("gorm:after_create").Register("pinpoint:after_create", after)

	update := db.Callback().Update()
	_ = update.Before("gorm:before_update").Register("pinpoint:before_update", wrapBefore("gorm.update"))
	_ = update.After("gorm:after_update").Register("pinpoint:after_update", after)

	delete := db.Callback().Delete()
	_ = delete.Before("gorm:before_delete").Register("pinpoint:before_delete", wrapBefore("gorm.delete"))
	_ = delete.After("gorm:after_delete").Register("pinpoint:after_delete", after)

	query := db.Callback().Query()
	_ = query.Before("gorm:query").Register("pinpoint:before_query", wrapBefore("gorm.query"))
	_ = query.After("gorm:after_query").Register("pinpoint:after_query", after)

	row := db.Callback().Row()
	_ = row.Before("gorm:row").Register("pinpoint:before_row", wrapBefore("gorm.row"))
	_ = row.After("gorm:row").Register("pinpoint:after_row", after)

	raw := db.Callback().Raw()
	_ = raw.Before("gorm:raw").Register("pinpoint:before_raw", wrapBefore("gorm.raw"))
	_ = raw.After("gorm:raw").Register("pinpoint:after_raw", after)
}

func wrapBefore(operationName string) func(*gorm.DB) {
	return func(scope *gorm.DB) {
		before(scope, operationName)
	}
}

func before(db *gorm.DB, operationName string) {
	// FromContext never returns nil - it falls back to the noop tracer - so
	// gate on sampling, which is what actually decides whether anything is
	// recorded.
	if tracer := pinpoint.FromContext(db.Statement.Context); tracer.IsSampled() {
		span := tracer.NewSpanEvent(operationName)
		span.SpanEvent().SetServiceType(pinpoint.ServiceTypeGoFunction)
	}
}

func after(db *gorm.DB) {
	if tracer := pinpoint.FromContext(db.Statement.Context); tracer.IsSampled() {
		// A miss is a normal outcome, not a failure: gorm sets ErrRecordNotFound
		// on First, Take and Last before the after callbacks run, and recording
		// it marked every not-found lookup as a failed transaction. Same rule
		// as the redis and memcache plugins apply to their cache misses.
		if !errors.Is(db.Error, gorm.ErrRecordNotFound) {
			tracer.SpanEvent().SetError(db.Error)
		}
		tracer.EndSpanEvent()
	}
}
