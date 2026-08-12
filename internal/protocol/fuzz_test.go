package protocol

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func FuzzDecodeControl(f *testing.F) {
	f.Add([]byte(`{"type":"handshake","payload":{"version":"3.0","client_id":"brig","terminal_size":[80,24],"cwd":"/tmp"}}`))
	f.Add([]byte(`{"type":"list"}`))
	f.Add([]byte(`{"type":"create","payload":{"name":"braw","agent":"claude","repo_path":"/croft"}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"type":""}`))
	f.Add([]byte(`{"type":"unknown","payload":null}`))
	f.Add([]byte(`{`))
	f.Add([]byte(``))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"type":123}`))
	f.Add([]byte(`{"type":"x","payload":"not an object"}`))
	f.Add([]byte("\x00\x00\x00"))

	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := DecodeControl(data)
		if err != nil {
			return
		}
		// If decoding succeeded, the envelope should be usable.
		_ = env.Type
		_ = env.Payload

		// Round-trip: re-encode and re-decode should not lose the type.
		reEncoded, err := EncodeControl(env.Type, env.Payload)
		if err != nil {
			return
		}

		env2, err := DecodeControl(reEncoded)
		if err != nil {
			t.Fatalf("round-trip decode failed: %v", err)
		}

		if env2.Type != env.Type {
			t.Fatalf("round-trip type mismatch: %q vs %q", env2.Type, env.Type)
		}
	})
}

func FuzzReadFrame(f *testing.F) {
	// Valid frame: channel=0x00, length=15, payload=`{"type":"list"}`
	validPayload := []byte(`{"type":"list"}`)

	var validFrame bytes.Buffer
	validFrame.WriteByte(ChannelControl)
	validFrame.Write([]byte{0, 0, 0, byte(len(validPayload))}) //nolint:gosec // G115: validPayload is a fixed 15-byte test literal
	validFrame.Write(validPayload)
	f.Add(validFrame.Bytes())

	// Valid data channel frame
	var dataFrame bytes.Buffer
	dataFrame.WriteByte(ChannelData)
	dataFrame.Write([]byte{0, 0, 0, 7})
	dataFrame.Write([]byte("blether"))
	f.Add(dataFrame.Bytes())

	// Empty payload frame
	var emptyFrame bytes.Buffer
	emptyFrame.WriteByte(ChannelControl)
	emptyFrame.Write([]byte{0, 0, 0, 0})
	f.Add(emptyFrame.Bytes())

	// Too-short data
	f.Add([]byte{0x01})
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0})

	// Header claiming oversized payload
	f.Add([]byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF})

	f.Fuzz(func(t *testing.T, data []byte) {
		reader := NewFrameReader(bytes.NewReader(data))

		frame, err := reader.ReadFrame()
		if err != nil {
			return
		}

		// If we got a valid frame, verify round-trip.
		var buf bytes.Buffer

		writer := NewFrameWriter(&buf)
		if err := writer.WriteFrame(frame.Channel, frame.Payload); err != nil {
			return
		}

		reader2 := NewFrameReader(&buf)

		frame2, err := reader2.ReadFrame()
		if err != nil {
			t.Fatalf("round-trip read failed: %v", err)
		}

		if frame2.Channel != frame.Channel {
			t.Fatalf("round-trip channel mismatch: %d vs %d", frame2.Channel, frame.Channel)
		}

		if !bytes.Equal(frame2.Payload, frame.Payload) {
			t.Fatalf("round-trip payload mismatch")
		}
	})
}

func FuzzDecodePayload(f *testing.F) {
	targets := protocolFuzzPayloadTargets(f)
	for _, seed := range protocolFuzzPayloadSeeds(f, targets) {
		f.Add([]byte(seed.messageType), seed.payload)
	}

	f.Fuzz(func(t *testing.T, msgType, payloadRaw []byte) {
		if len(msgType) > maxFuzzMessageTypeBytes || len(payloadRaw) > maxFuzzPayloadBytes {
			t.Skip()
		}

		targetTypes := targets[string(msgType)]
		if len(targetTypes) == 0 {
			return
		}

		env := Envelope{
			Type:    string(msgType),
			Payload: payloadRaw,
		}

		for _, targetType := range targetTypes {
			decoded := reflect.New(targetType)
			if err := DecodePayload(env, decoded.Interface()); err != nil {
				continue
			}

			encoded, err := EncodeControl(env.Type, decoded.Interface())
			if err != nil {
				t.Fatalf("EncodeControl(%q, %s): %v", env.Type, targetType.Name(), err)
			}

			roundTripped, err := DecodeControl(encoded)
			if err != nil {
				t.Fatalf("DecodeControl(%q, %s round-trip): %v", env.Type, targetType.Name(), err)
			}

			if roundTripped.Type != env.Type {
				t.Fatalf("round-trip type = %q, want %q", roundTripped.Type, env.Type)
			}

			if len(roundTripped.Payload) == 0 || !json.Valid(roundTripped.Payload) {
				t.Fatalf("round-trip payload for %q/%s is not valid JSON: %q", env.Type, targetType.Name(), roundTripped.Payload)
			}

			decodedAgain := reflect.New(targetType)
			if err := DecodePayload(roundTripped, decodedAgain.Interface()); err != nil {
				t.Fatalf("DecodePayload(%q, %s round-trip): %v", env.Type, targetType.Name(), err)
			}
		}
	})
}

func TestProtocolFuzzPayloadTargetsCoverRegisteredTypes(t *testing.T) {
	targets := protocolFuzzPayloadTargets(t)

	for _, registered := range registeredTypes {
		rt := reflect.TypeOf(registered)
		if !protocolFuzzTargetIncludes(targets[rt.Name()], rt) {
			t.Errorf("registered type %s has no type-name fuzz target", rt.Name())
		}

		wireName := protocolFuzzWireName(rt.Name())
		if wireName != "" && !protocolFuzzTargetIncludes(targets[wireName], rt) {
			t.Errorf("registered type %s has no wire-name fuzz target %q", rt.Name(), wireName)
		}
	}
}

func TestProtocolManifestFieldNamesAreStable(t *testing.T) {
	manifest, err := BuildManifest()
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}

	manifestTypes := make(map[string]bool, len(manifest.Types))
	for _, mt := range manifest.Types {
		if strings.TrimSpace(mt.Name) == "" {
			t.Fatal("manifest contains a type with an empty name")
		}

		manifestTypes[mt.Name] = true
	}

	for _, mt := range manifest.Types {
		seenFields := map[string]bool{}

		for _, field := range mt.Fields {
			if strings.TrimSpace(field.Name) == "" {
				t.Errorf("%s has a manifest field with an empty JSON name", mt.Name)
			}

			if seenFields[field.Name] {
				t.Errorf("%s has duplicate manifest field %q", mt.Name, field.Name)
			}

			seenFields[field.Name] = true

			assertProtocolManifestFieldType(t, mt.Name+"."+field.Name, field.Type, manifestTypes)
		}
	}
}

const (
	maxFuzzMessageTypeBytes = 128
	maxFuzzPayloadBytes     = 32 * 1024
	maxFuzzSampleDepth      = 3
)

type protocolFuzzHelper interface {
	Helper()
	Fatalf(format string, args ...any)
}

type protocolFuzzPayloadSeed struct {
	messageType string
	payload     []byte
}

var protocolFuzzWireAliases = []struct {
	messageType string
	payload     any
}{
	{"attached", SessionInfo{}},
	{"auth_ok", HandshakeOkMsg{}},
	{"deleted", DeleteResultMsg{}},
	{"diagnostics", DiagnosticsMsg{}},
	{"event", EventMsg{}},
	{"events_following", EventFollowingResponseMsg{}},
	{"msg_message", ConversationMessage{}},
	{"restored", RestoreResultMsg{}},
	{"scenario_result_published", ScenarioResultPublishResponse{}},
	{"session_update", SessionInfo{}},
	{"terminal_owned_attached", TerminalOwnedAttachSeedMsg{}},
}

var protocolMalformedPayloadSeeds = [][]byte{
	[]byte(``),
	[]byte(`null`),
	[]byte(`"braw"`),
	[]byte(`42`),
	[]byte(`true`),
	[]byte(`[]`),
	[]byte(`{"unknown":"braw"}`),
	[]byte(`{"session_id":{"nested":"object"},"terminal_size":{"cols":80},"labels":{"not":"array"},"results":{"not":"array"}}`),
	[]byte(`{not json`),
}

var protocolMalformedPayloadMessageTypes = []string{
	"Envelope",
	"ScenarioStartMsg",
	"TerminalOwnedAttachSeedMsg",
	"create",
	"handshake",
}

var protocolFixturePayloadSeeds = []protocolFuzzPayloadSeed{
	{messageType: "handshake", payload: []byte(`{"version":"3.0","client_id":"brig","terminal_size":[80,24],"cwd":"/tmp"}`)},
	{messageType: "create", payload: []byte(`{"name":"braw","agent":"claude"}`)},
	{messageType: "attach", payload: []byte(`{"session_id":"abc123"}`)},
	{messageType: "resize", payload: []byte(`{"cols":120,"rows":40}`)},
	{messageType: "type", payload: []byte(`{"session_id":"brig","input":"blether","no_newline":true}`)},
	{messageType: "bad", payload: []byte(`{not json`)},
	{messageType: "empty", payload: []byte(``)},
	{messageType: "null", payload: []byte(`null`)},
}

func protocolFuzzPayloadTargets(t protocolFuzzHelper) map[string][]reflect.Type {
	t.Helper()

	targets := map[string][]reflect.Type{}
	registered := map[reflect.Type]bool{}

	for _, payload := range registeredTypes {
		rt := reflect.TypeOf(payload)
		if rt.Kind() != reflect.Struct {
			t.Fatalf("registered type %s is not a struct", rt)
		}

		registered[rt] = true
		addProtocolFuzzPayloadTarget(targets, rt.Name(), rt)
		addProtocolFuzzPayloadTarget(targets, protocolFuzzWireName(rt.Name()), rt)
	}

	for _, alias := range protocolFuzzWireAliases {
		rt := reflect.TypeOf(alias.payload)
		if !registered[rt] {
			t.Fatalf("wire alias %q points at unregistered type %s", alias.messageType, rt.Name())
		}

		addProtocolFuzzPayloadTarget(targets, alias.messageType, rt)
	}

	return targets
}

func protocolFuzzPayloadSeeds(t protocolFuzzHelper, targets map[string][]reflect.Type) []protocolFuzzPayloadSeed {
	t.Helper()

	keys := sortedProtocolFuzzTargetKeys(targets)
	seeds := []protocolFuzzPayloadSeed{}
	seen := map[string]bool{}

	for _, messageType := range keys {
		targetTypes := sortedProtocolFuzzTargetTypes(targets[messageType])
		for _, targetType := range targetTypes {
			if messageType == targetType.Name() {
				appendProtocolFuzzPayloadSeed(&seeds, seen, messageType, []byte(`{}`))
			}

			appendProtocolFuzzPayloadSeed(&seeds, seen, messageType, protocolFuzzSamplePayloadJSON(t, targetType))
		}
	}

	for _, messageType := range protocolMalformedPayloadMessageTypes {
		for _, payload := range protocolMalformedPayloadSeeds {
			appendProtocolFuzzPayloadSeed(&seeds, seen, messageType, payload)
		}
	}

	for _, seed := range protocolFixturePayloadSeeds {
		appendProtocolFuzzPayloadSeed(&seeds, seen, seed.messageType, seed.payload)
	}

	return seeds
}

func addProtocolFuzzPayloadTarget(targets map[string][]reflect.Type, messageType string, targetType reflect.Type) {
	if messageType == "" {
		return
	}

	if protocolFuzzTargetIncludes(targets[messageType], targetType) {
		return
	}

	targets[messageType] = append(targets[messageType], targetType)
}

func protocolFuzzTargetIncludes(targets []reflect.Type, targetType reflect.Type) bool {
	for _, existing := range targets {
		if existing == targetType {
			return true
		}
	}

	return false
}

func appendProtocolFuzzPayloadSeed(seeds *[]protocolFuzzPayloadSeed, seen map[string]bool, messageType string, payload []byte) {
	key := messageType + "\x00" + string(payload)
	if seen[key] {
		return
	}

	copied := append([]byte(nil), payload...)
	*seeds = append(*seeds, protocolFuzzPayloadSeed{messageType: messageType, payload: copied})
	seen[key] = true
}

func sortedProtocolFuzzTargetKeys(targets map[string][]reflect.Type) []string {
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func sortedProtocolFuzzTargetTypes(targets []reflect.Type) []reflect.Type {
	out := append([]reflect.Type(nil), targets...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })

	return out
}

func protocolFuzzWireName(typeName string) string {
	name := strings.TrimSuffix(typeName, "Msg")
	if name == "" {
		return ""
	}

	var out strings.Builder

	for i := 0; i < len(name); i++ {
		c := name[i]
		if i > 0 && isProtocolFuzzUpper(c) {
			prev := name[i-1]

			var next byte
			if i+1 < len(name) {
				next = name[i+1]
			}

			if isProtocolFuzzLower(prev) || isProtocolFuzzDigit(prev) ||
				(isProtocolFuzzUpper(prev) && isProtocolFuzzLower(next)) {
				out.WriteByte('_')
			}
		}

		out.WriteByte(toProtocolFuzzLower(c))
	}

	return out.String()
}

func protocolFuzzSamplePayloadJSON(t protocolFuzzHelper, targetType reflect.Type) []byte {
	t.Helper()

	sample := protocolFuzzSampleValue(targetType, 0)

	payload, err := json.Marshal(sample.Interface())
	if err != nil {
		t.Fatalf("marshal sample %s: %v", targetType.Name(), err)
	}

	if len(payload) == 0 || !json.Valid(payload) {
		t.Fatalf("sample payload for %s is not valid JSON: %q", targetType.Name(), payload)
	}

	return payload
}

func protocolFuzzSampleValue(rt reflect.Type, depth int) reflect.Value {
	if rt == rawMessageType {
		return reflect.ValueOf(json.RawMessage(`{"braw":true}`))
	}

	if depth > maxFuzzSampleDepth {
		return reflect.Zero(rt)
	}

	switch rt.Kind() {
	case reflect.Pointer:
		elem := protocolFuzzSampleValue(rt.Elem(), depth+1)
		ptr := reflect.New(rt.Elem())
		ptr.Elem().Set(elem)

		return ptr
	case reflect.Struct:
		value := reflect.New(rt).Elem()
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if field.PkgPath != "" {
				continue
			}

			_, _, skip := jsonKey(field)
			if skip {
				continue
			}

			fieldValue := value.Field(i)
			if !fieldValue.CanSet() {
				continue
			}

			sample := protocolFuzzSampleValue(field.Type, depth+1)
			if sample.Type().AssignableTo(field.Type) {
				fieldValue.Set(sample)
			}
		}

		return value
	case reflect.Slice:
		value := reflect.MakeSlice(rt, 1, 1)
		value.Index(0).Set(protocolFuzzSampleValue(rt.Elem(), depth+1))

		return value
	case reflect.Array:
		value := reflect.New(rt).Elem()
		for i := 0; i < rt.Len(); i++ {
			value.Index(i).Set(protocolFuzzSampleValue(rt.Elem(), depth+1))
		}

		return value
	case reflect.Map:
		return reflect.MakeMap(rt)
	case reflect.String:
		return reflect.ValueOf("braw").Convert(rt)
	case reflect.Bool:
		return reflect.ValueOf(true).Convert(rt)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value := reflect.New(rt).Elem()
		value.SetInt(7)

		return value
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value := reflect.New(rt).Elem()
		value.SetUint(7)

		return value
	case reflect.Float32, reflect.Float64:
		value := reflect.New(rt).Elem()
		value.SetFloat(4.25)

		return value
	default:
		return reflect.Zero(rt)
	}
}

func assertProtocolManifestFieldType(t *testing.T, fieldPath string, fieldType FieldType, manifestTypes map[string]bool) {
	t.Helper()

	if strings.TrimSpace(fieldType.Kind) == "" {
		t.Errorf("%s has an empty manifest field kind", fieldPath)
	}

	switch fieldType.Kind {
	case "array":
		if fieldType.Elem == nil {
			t.Errorf("%s array field has no element type", fieldPath)
			return
		}

		assertProtocolManifestFieldType(t, fieldPath+"[]", *fieldType.Elem, manifestTypes)
	case "object":
		if strings.TrimSpace(fieldType.Ref) == "" {
			t.Errorf("%s object field has an empty ref", fieldPath)
			return
		}

		if !manifestTypes[fieldType.Ref] {
			t.Errorf("%s object field references unregistered manifest type %q", fieldPath, fieldType.Ref)
		}
	case "bool", "float", "int", "map", "raw", "string":
	default:
		t.Errorf("%s has unsupported manifest field kind %q", fieldPath, fieldType.Kind)
	}
}

func isProtocolFuzzUpper(c byte) bool {
	return c >= 'A' && c <= 'Z'
}

func isProtocolFuzzLower(c byte) bool {
	return c >= 'a' && c <= 'z'
}

func isProtocolFuzzDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func toProtocolFuzzLower(c byte) byte {
	if isProtocolFuzzUpper(c) {
		return c + ('a' - 'A')
	}

	return c
}
