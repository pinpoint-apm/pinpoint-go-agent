// Package ppgorm instruments the go-gorm/gorm package (https://github.com/go-gorm/gorm).
//
// This package instruments the go-gorm/gorm calls.
// Use the Open as the gorm.Open.
//
//	g, err := ppgorm.Open(mysql.New(mysql.Config{Conn: db}), &gorm.Config{})
//
// It is necessary to pass the context containing the pinpoint.Tracer to gorm.DB.
//
//	g = g.WithContext(pinpoint.NewContext(context.Background(), tracer))
//	g.Create(&Product{Code: "D42", Price: 100})
package ppgorm

import (
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"gorm.io/gorm"
)

// Open returns a new *gorm.DB ready to instrument.
func Open(dialector gorm.Dialector, opts ...gorm.Option) (*gorm.DB, error) {
	db, err := gorm.Open(dialector, opts...)
	if err != nil {
		return db, err
	}

	registerCallbacks(db)
	return db, err
}

func registerCallbacks(db *gorm.DB) {
	//The name of callback(before/after) should be matched with default callback of GORM
	//https://github.com/go-gorm/gorm/blob/master/callbacks/callbacks.go

	create := db.Callback().Create()
	_ = create.Before("gorm:before_create").Register("pinpoint:before_create", wrapBefore("gorm.create"))
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
	if tracer := pinpoint.FromContext(db.Statement.Context); tracer != nil {
		span := tracer.NewSpanEvent(operationName)
		span.SpanEvent().SetServiceType(pinpoint.ServiceTypeGoFunction)
	}
}

func after(db *gorm.DB) {
	if tracer := pinpoint.FromContext(db.Statement.Context); tracer != nil {
		tracer.SpanEvent().SetError(db.Error)
		tracer.EndSpanEvent()
	}
}
