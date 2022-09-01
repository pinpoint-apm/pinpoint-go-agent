package pinpoint

import (
	"github.com/golang/protobuf/ptypes/wrappers"
	pb "github.com/pinpoint-apm/pinpoint-go-agent/protobuf"
)

const (
	AnnotationArgs0   = -1
	AnnotationApi     = 12
	AnnotationSqlId   = 20
	AnnotationHttpUrl = 40
	//AnnotationHttpParam        = 41
	AnnotationHttpCookie         = 45
	AnnotationHttpStatusCode     = 46
	AnnotationHttpRequestHeader  = 47
	AnnotationHttpResponseHeader = 55
	AnnotationProxyHttpHeader    = 300
)

type annotation struct {
	list []*pb.PAnnotation
}

func (a *annotation) AppendInt(key int32, i int32) {
	a.list = append(a.list, &pb.PAnnotation{
		Key: key,
		Value: &pb.PAnnotationValue{
			Field: &pb.PAnnotationValue_IntValue{
				IntValue: i,
			},
		},
	})
}

func (a *annotation) AppendString(key int32, s string) {
	a.list = append(a.list, &pb.PAnnotation{
		Key: key,
		Value: &pb.PAnnotationValue{
			Field: &pb.PAnnotationValue_StringValue{
				StringValue: s,
			},
		},
	})
}

func (a *annotation) AppendStringString(key int32, s1 string, s2 string) {
	a.list = append(a.list, &pb.PAnnotation{
		Key: key,
		Value: &pb.PAnnotationValue{
			Field: &pb.PAnnotationValue_StringStringValue{
				StringStringValue: &pb.PStringStringValue{
					StringValue1: &wrappers.StringValue{Value: s1},
					StringValue2: &wrappers.StringValue{Value: s2},
				},
			},
		},
	})
}

func (a *annotation) AppendIntStringString(key int32, i int32, s1 string, s2 string) {
	a.list = append(a.list, &pb.PAnnotation{
		Key: key,
		Value: &pb.PAnnotationValue{
			Field: &pb.PAnnotationValue_IntStringStringValue{
				IntStringStringValue: &pb.PIntStringStringValue{
					IntValue:     i,
					StringValue1: &wrappers.StringValue{Value: s1},
					StringValue2: &wrappers.StringValue{Value: s2},
				},
			},
		},
	})
}

func (a *annotation) AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string) {
	a.list = append(a.list, &pb.PAnnotation{
		Key: key,
		Value: &pb.PAnnotationValue{
			Field: &pb.PAnnotationValue_LongIntIntByteByteStringValue{
				LongIntIntByteByteStringValue: &pb.PLongIntIntByteByteStringValue{
					LongValue:   l,
					IntValue1:   i1,
					IntValue2:   i2,
					ByteValue1:  b1,
					ByteValue2:  b2,
					StringValue: &wrappers.StringValue{Value: s},
				},
			},
		},
	})
}
