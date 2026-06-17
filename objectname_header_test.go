package pinpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// agentWith builds a minimal agent carrying the given resolved object name,
// for exercising the gRPC header builder.
func agentWith(o *objectName) *agent {
	return &agent{
		appName:     o.applicationName,
		appType:     ServiceTypeGoApp,
		agentID:     o.agentID,
		agentName:   o.agentName,
		serviceName: o.serviceName,
		objName:     o,
		startTime:   12345,
	}
}

func TestAgentHeaderMap_V1V3(t *testing.T) {
	for _, ver := range []nameVersion{nameV1, nameV3} {
		o := &objectName{
			version:         ver,
			agentID:         "agent-id",
			agentName:       "agent-name",
			applicationName: "app",
		}
		m := agentHeaderMap(agentWith(o))

		assert.Equal(t, "app", m[headerAppName])
		assert.Equal(t, "agent-id", m[headerAgentID])
		assert.Equal(t, "agent-name", m[headerAgentName])
		assert.Equal(t, "12345", m[headerStartTime])
		assert.Equal(t, "1800", m[headerServiceType]) // ServiceTypeGoApp
		assert.Equal(t, "100", m[headerProtocolVersion])

		// v1/v3 must NOT send servicename or apikey.
		_, hasServiceName := m[headerServiceName]
		_, hasApiKey := m[headerApiKey]
		assert.False(t, hasServiceName, "v1/v3 must not send servicename")
		assert.False(t, hasApiKey, "v1/v3 must not send apikey")
	}
}

func TestAgentHeaderMap_V1V3_AgentNameOptional(t *testing.T) {
	// When agentName is empty (only possible if explicitly cleared), v1/v3 omit it.
	o := &objectName{version: nameV3, agentID: "agent-id", applicationName: "app"}
	m := agentHeaderMap(agentWith(o))
	_, hasAgentName := m[headerAgentName]
	assert.False(t, hasAgentName, "empty agentName is omitted for v1/v3")
}

func TestAgentHeaderMap_V4(t *testing.T) {
	o := &objectName{
		version:         nameV4,
		agentID:         "uuid-base64",
		agentName:       "agent-name",
		applicationName: "app",
		serviceName:     "svc",
		apiKey:          "secret",
	}
	m := agentHeaderMap(agentWith(o))

	assert.Equal(t, "app", m[headerAppName])
	assert.Equal(t, "uuid-base64", m[headerAgentID])
	assert.Equal(t, "agent-name", m[headerAgentName])
	assert.Equal(t, "svc", m[headerServiceName])
	assert.Equal(t, "secret", m[headerApiKey])
	assert.Equal(t, "12345", m[headerStartTime])
	assert.Equal(t, "1800", m[headerServiceType])
	assert.Equal(t, "400", m[headerProtocolVersion])
}

func TestSpanInject_ServiceNamePropagation(t *testing.T) {
	// v4 agent with a serviceName injects Pinpoint-pServiceName.
	span := defaultTestSpan()
	span.agent.serviceName = "MyService"
	span.NewSpanEvent("op")

	m := map[string]string{}
	span.Inject(&DistributedTracingContextMap{m})
	assert.Equal(t, "MyService", m[HeaderParentServiceName])
}

func TestSpanInject_NoServiceName_OmitsHeader(t *testing.T) {
	// v1/v3 agent (no serviceName) injects no Pinpoint-pServiceName header.
	span := defaultTestSpan()
	span.agent.serviceName = ""
	span.NewSpanEvent("op")

	m := map[string]string{}
	span.Inject(&DistributedTracingContextMap{m})
	_, ok := m[HeaderParentServiceName]
	assert.False(t, ok, "no serviceName -> no Pinpoint-pServiceName header")
}

func TestSpanExtract_ServiceName(t *testing.T) {
	span := defaultTestSpan()
	reader := &DistributedTracingContextMap{m: map[string]string{
		HeaderParentServiceName: "UpstreamService",
	}}
	span.Extract(reader)
	assert.Equal(t, "UpstreamService", span.parentServiceName)
}

func TestSpanInjectExtract_ServiceNameRoundTrip(t *testing.T) {
	sender := defaultTestSpan()
	sender.agent.serviceName = "ServiceA"
	sender.NewSpanEvent("op")

	carrier := map[string]string{}
	sender.Inject(&DistributedTracingContextMap{carrier})

	receiver := defaultTestSpan()
	receiver.Extract(&DistributedTracingContextMap{carrier})
	assert.Equal(t, "ServiceA", receiver.parentServiceName)
}
