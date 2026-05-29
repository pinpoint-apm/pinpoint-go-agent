package pinpoint

import (
	"sync"

	"github.com/golang/protobuf/ptypes/wrappers"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
)

// annotation stores recorded annotations as compact value structs instead of
// eagerly building the protobuf object graph. Append* runs on the application
// hot path, so it only copies the raw values into a slice (one amortized
// allocation) rather than allocating the 3-8 protobuf objects each annotation
// shape needs. The protobuf messages are materialized lazily in getList(),
// which the agent calls on its sender goroutine at serialization time.
type annotation struct {
	values         []annotationValue
	annotationLock sync.Mutex
}

type annotationValueType uint8

const (
	annotationTypeInt annotationValueType = iota
	annotationTypeLong
	annotationTypeString
	annotationTypeStringString
	annotationTypeIntStringString
	annotationTypeBytesStringString
	annotationTypeLongIntIntByteByteString
)

// annotationValue holds the raw fields for every annotation shape. It is stored
// by value in a slice; only the fields relevant to typ are populated.
type annotationValue struct {
	key   int32
	typ   annotationValueType
	i1    int32
	i2    int32
	b1    int32
	b2    int32
	l     int64
	s1    string
	s2    string
	bytes []byte
}

func (a *annotation) append(v annotationValue) {
	a.annotationLock.Lock()
	a.values = append(a.values, v)
	a.annotationLock.Unlock()
}

func (a *annotation) AppendInt(key int32, i int32) {
	a.append(annotationValue{key: key, typ: annotationTypeInt, i1: i})
}

func (a *annotation) AppendLong(key int32, l int64) {
	a.append(annotationValue{key: key, typ: annotationTypeLong, l: l})
}

func (a *annotation) AppendString(key int32, s string) {
	a.append(annotationValue{key: key, typ: annotationTypeString, s1: s})
}

func (a *annotation) AppendStringString(key int32, s1 string, s2 string) {
	a.append(annotationValue{key: key, typ: annotationTypeStringString, s1: s1, s2: s2})
}

func (a *annotation) AppendIntStringString(key int32, i int32, s1 string, s2 string) {
	a.append(annotationValue{key: key, typ: annotationTypeIntStringString, i1: i, s1: s1, s2: s2})
}

func (a *annotation) AppendBytesStringString(key int32, bs []byte, s1 string, s2 string) {
	a.append(annotationValue{key: key, typ: annotationTypeBytesStringString, bytes: bs, s1: s1, s2: s2})
}

func (a *annotation) AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string) {
	a.append(annotationValue{key: key, typ: annotationTypeLongIntIntByteByteString, l: l, i1: i1, i2: i2, b1: b1, b2: b2, s1: s})
}

// toProto builds the protobuf annotation for a stored value. Called off the hot
// path (sender goroutine) during serialization.
func (v *annotationValue) toProto() *pb.PAnnotation {
	value := &pb.PAnnotationValue{}

	switch v.typ {
	case annotationTypeInt:
		value.Field = &pb.PAnnotationValue_IntValue{IntValue: v.i1}
	case annotationTypeLong:
		value.Field = &pb.PAnnotationValue_LongValue{LongValue: v.l}
	case annotationTypeString:
		value.Field = &pb.PAnnotationValue_StringValue{StringValue: v.s1}
	case annotationTypeStringString:
		value.Field = &pb.PAnnotationValue_StringStringValue{
			StringStringValue: &pb.PStringStringValue{
				StringValue1: &wrappers.StringValue{Value: v.s1},
				StringValue2: &wrappers.StringValue{Value: v.s2},
			},
		}
	case annotationTypeIntStringString:
		value.Field = &pb.PAnnotationValue_IntStringStringValue{
			IntStringStringValue: &pb.PIntStringStringValue{
				IntValue:     v.i1,
				StringValue1: &wrappers.StringValue{Value: v.s1},
				StringValue2: &wrappers.StringValue{Value: v.s2},
			},
		}
	case annotationTypeBytesStringString:
		value.Field = &pb.PAnnotationValue_BytesStringStringValue{
			BytesStringStringValue: &pb.PBytesStringStringValue{
				BytesValue:   v.bytes,
				StringValue1: &wrappers.StringValue{Value: v.s1},
				StringValue2: &wrappers.StringValue{Value: v.s2},
			},
		}
	case annotationTypeLongIntIntByteByteString:
		value.Field = &pb.PAnnotationValue_LongIntIntByteByteStringValue{
			LongIntIntByteByteStringValue: &pb.PLongIntIntByteByteStringValue{
				LongValue:   v.l,
				IntValue1:   v.i1,
				IntValue2:   v.i2,
				ByteValue1:  v.b1,
				ByteValue2:  v.b2,
				StringValue: &wrappers.StringValue{Value: v.s1},
			},
		}
	}

	return &pb.PAnnotation{Key: v.key, Value: value}
}

func (a *annotation) getList() []*pb.PAnnotation {
	a.annotationLock.Lock()
	defer a.annotationLock.Unlock()

	if len(a.values) == 0 {
		return nil
	}

	list := make([]*pb.PAnnotation, len(a.values))
	for i := range a.values {
		list[i] = a.values[i].toProto()
	}
	return list
}
