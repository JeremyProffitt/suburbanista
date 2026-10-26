package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

var TemplateRequired = []string{"template-structure", "resource-policy", "runtime-policy", "queue-schedule-policy", "table-schema-policy"}

// Template checks static configuration only. Optional baseline is the actual
// deployed template, retained after first creation to freeze table key schemas.
func Template(data []byte, path string, baseline []byte, commit, runID string, now time.Time) Report {
	r := Report{SchemaVersion: "1", Mode: "template", Commit: commit, RunID: runID, GeneratedAt: now.UTC()}
	digest := sha256.Sum256(data)
	evidence := []Evidence{{Path: path, SHA256: fmt.Sprintf("%x", digest)}}
	issues := map[string][]string{}
	add := func(group, message string) { issues[group] = append(issues[group], message) }
	doc, err := parseTemplate(data)
	if err != nil {
		add("template-structure", "invalid YAML or duplicate mapping key")
	}
	resources := mapping(doc["Resources"])
	if len(resources) == 0 {
		add("template-structure", "template must contain resources")
	}
	walkTemplate(doc, func(key string, value any) {
		switch key {
		case "KmsKeyArn", "KmsKeyId", "VpcConfig", "ProvisionedConcurrencyConfig", "PointInTimeRecoverySpecification", "BackupPolicy":
			add("resource-policy", "forbidden property: "+key)
		case "Tracing":
			if textValue(value) == "Active" {
				add("resource-policy", "active tracing forbidden")
			}
		case "TracingEnabled":
			if value == true {
				add("resource-policy", "active tracing forbidden")
			}
		case "Tier":
			if textValue(value) == "Advanced" {
				add("resource-policy", "advanced parameter tier forbidden")
			}
		}
		if s, ok := value.(string); ok && strings.Contains(s, "LambdaInsightsExtension") {
			add("resource-policy", "Lambda Insights forbidden")
		}
	})
	workerTimeout := 0
	workerPresent := false
	roles := map[string]string{}
	queueVisibility := 0
	vehicleQueuePresent := false
	for name, raw := range resources {
		resource := mapping(raw)
		kind := textValue(resource["Type"])
		p := mapping(resource["Properties"])
		for _, prefix := range []string{"AWS::SecretsManager::", "AWS::KMS::", "AWS::EC2::", "AWS::AutoScaling::", "AWS::Backup::"} {
			if strings.HasPrefix(kind, prefix) {
				add("resource-policy", name+": prohibited resource")
			}
		}
		if kind == "AWS::CloudWatch::Alarm" || kind == "AWS::CloudWatch::Dashboard" || kind == "AWS::Serverless::Api" {
			add("resource-policy", name+": prohibited resource")
		}
		switch kind {
		case "AWS::Logs::LogGroup":
			if number(p["RetentionInDays"]) != 7 {
				add("resource-policy", name+": log retention must be seven days")
			}
		case "AWS::S3::Bucket":
			if _, ok := p["VersioningConfiguration"]; ok && !hasNoncurrentExpiry(mapping(p["LifecycleConfiguration"])) {
				add("resource-policy", name+": versioning requires noncurrent version expiration")
			}
		case "AWS::DynamoDB::Table":
			if textValue(p["BillingMode"]) != "PAY_PER_REQUEST" || p["DeletionProtectionEnabled"] != true || textValue(resource["DeletionPolicy"]) != "Retain" || textValue(resource["UpdateReplacePolicy"]) != "Retain" {
				add("resource-policy", name+": table protection or billing policy differs")
			}
			if _, ok := p["TableName"]; ok {
				add("resource-policy", name+": table names must be generated")
			}
			if _, ok := p["SSESpecification"]; ok {
				add("resource-policy", name+": use AWS-owned table encryption")
			}
			ttl := mapping(p["TimeToLiveSpecification"])
			if ttl["Enabled"] != true || textValue(ttl["AttributeName"]) != "expiresAt" {
				add("resource-policy", name+": TTL must use expiresAt")
			}
		case "AWS::Serverless::Function", "AWS::Lambda::Function":
			merged := map[string]any{}
			if kind == "AWS::Serverless::Function" {
				for k, v := range mapping(mapping(doc["Globals"])["Function"]) {
					merged[k] = v
				}
			}
			for k, v := range p {
				merged[k] = v
			}
			p = merged
			if !reflect.DeepEqual(p["Architectures"], []any{"arm64"}) {
				add("runtime-policy", name+": Lambda architecture must be arm64")
			}
			if !ownedLog(mapping(p["LoggingConfig"])["LogGroup"], resources) {
				add("runtime-policy", name+": function must bind an owned seven-day log group")
			}
			if p["Role"] == nil {
				add("runtime-policy", name+": explicit per-function role required")
			}
			roleBytes, _ := json.Marshal(p["Role"])
			if prior, exists := roles[string(roleBytes)]; exists {
				add("runtime-policy", name+": runtime role shared with "+prior)
			}
			roles[string(roleBytes)] = name
			if policies, ok := p["Policies"]; ok {
				inspectPolicies(policies, name, add)
			}
			identity := strings.ToLower(name + " " + textValue(p["CodeUri"]) + " " + textValue(p["FunctionName"]))
			wantTimeout, wantMemory, wantReserved := 0, 0, 0
			switch {
			case strings.Contains(identity, "telemetry"):
				wantTimeout = 90
				wantMemory = 256
				wantReserved = 1
			case strings.Contains(identity, "worker"):
				workerPresent = true
				workerTimeout = number(p["Timeout"])
				wantTimeout = 120
				wantMemory = 256
				if _, ok := p["ReservedConcurrentExecutions"]; ok {
					add("runtime-policy", name+": worker must not reserve concurrency")
				}
			case strings.Contains(identity, "alexa"):
				wantTimeout = 7
				wantMemory = 256
				wantReserved = 2
				variables := mapping(mapping(p["Environment"])["Variables"])
				for _, key := range []string{"CREDENTIAL_ENCRYPTION_KEY_A", "CREDENTIAL_ENCRYPTION_KEY_B", "ONSTAR_DEVICE_ID", "ASK_REFRESH_TOKEN", "LWA_CLIENT_SECRET"} {
					if _, exists := variables[key]; exists {
						add("runtime-policy", name+": Alexa must not receive "+key)
					}
				}
			case strings.Contains(identity, "notify"):
				wantTimeout = 30
				wantMemory = 128
			case strings.Contains(identity, "api"):
				wantTimeout = 30
				wantMemory = 256
				wantReserved = 5
			}
			if wantTimeout > 0 && (number(p["Timeout"]) != wantTimeout || number(p["MemorySize"]) != wantMemory) {
				add("runtime-policy", name+": runtime timeout or memory differs")
			}
			if wantReserved > 0 && number(p["ReservedConcurrentExecutions"]) != wantReserved {
				add("runtime-policy", name+": required concurrency ceiling absent")
			}
			for _, rawEvent := range mapping(p["Events"]) {
				event := mapping(rawEvent)
				if textValue(event["Type"]) == "SQS" {
					checkEvent(mapping(event["Properties"]), name, add)
				}
				if textValue(event["Type"]) == "AlexaSkill" && !reflect.DeepEqual(mapping(event["Properties"])["SkillId"], map[string]any{"Ref": "AlexaSkillId"}) {
					add("runtime-policy", name+": Alexa trigger must use the configured skill ID")
				}
			}
		case "AWS::Lambda::EventSourceMapping":
			if strings.Contains(strings.ToLower(name), "worker") || p["ScalingConfig"] != nil {
				checkEvent(p, name, add)
			}
		case "AWS::IAM::Role":
			if len(sequence(p["ManagedPolicyArns"])) > 0 {
				add("resource-policy", name+": inline runtime policies required")
			}
			for _, policy := range sequence(p["Policies"]) {
				inspectPolicyDocument(mapping(policy)["PolicyDocument"], name, add)
			}
			if strings.Contains(strings.ToLower(name), "scheduler") {
				checkSchedulerTrust(p, name, add)
			}
		case "AWS::IAM::Policy":
			inspectPolicyDocument(p["PolicyDocument"], name, add)
		case "AWS::SQS::QueuePolicy":
			inspectPolicyDocument(p["PolicyDocument"], name, add)
			checkQueuePrincipals(p["PolicyDocument"], resources, name, add)
		case "AWS::SQS::Queue":
			queueName := textValue(p["QueueName"])
			isDLQ := strings.Contains(strings.ToLower(name+queueName), "dlq")
			if strings.Contains(strings.ToLower(name+queueName), "worker") && isDLQ && (p["FifoQueue"] != true || !strings.HasSuffix(queueName, ".fifo")) {
				add("queue-schedule-policy", name+": worker DLQ must be FIFO")
			}
			if strings.Contains(strings.ToLower(name+queueName), "scheduler") && isDLQ && (p["FifoQueue"] == true || strings.HasSuffix(queueName, ".fifo")) {
				add("queue-schedule-policy", name+": scheduler DLQ must be standard")
			}
			if isDLQ && number(p["MessageRetentionPeriod"]) != 604800 {
				add("queue-schedule-policy", name+": DLQ retention must be seven days")
			}
			if !isDLQ && p["FifoQueue"] == true {
				vehicleQueuePresent = true
				queueVisibility = number(p["VisibilityTimeout"])
				redrive := mapping(p["RedrivePolicy"])
				if p["ContentBasedDeduplication"] != true || p["SqsManagedSseEnabled"] != true || queueVisibility != 720 || number(p["MessageRetentionPeriod"]) != 86400 || number(redrive["maxReceiveCount"]) != 2 || redrive["deadLetterTargetArn"] == nil {
					add("queue-schedule-policy", name+": vehicle queue properties differ")
				}
			}
		case "AWS::Scheduler::Schedule":
			target := mapping(p["Target"])
			retry := mapping(target["RetryPolicy"])
			if name == "HourlyTelemetrySchedule" {
				if textValue(p["ScheduleExpression"]) != "cron(5 * * * ? *)" || textValue(p["State"]) != "ENABLED" || textValue(mapping(p["FlexibleTimeWindow"])["Mode"]) != "OFF" || number(retry["MaximumEventAgeInSeconds"]) != 60 || number(retry["MaximumRetryAttempts"]) != 0 || mapping(target["DeadLetterConfig"])["Arn"] == nil || target["SqsParameters"] != nil {
					add("queue-schedule-policy", name+": hourly telemetry schedule bounds differ")
				}
				for attribute, expected := range map[string]string{"Arn": "HourlyTelemetryFunction.Arn", "RoleArn": "HourlyTelemetrySchedulerRole.Arn"} {
					intrinsic := mapping(target[attribute])["Fn::GetAtt"]
					actual := textValue(intrinsic)
					if parts := sequence(intrinsic); len(parts) == 2 {
						actual = textValue(parts[0]) + "." + textValue(parts[1])
					}
					if actual != expected {
						add("queue-schedule-policy", name+": hourly telemetry target differs")
					}
				}
				break
			}
			if textValue(p["ScheduleExpression"]) != "cron(0,30 * * * ? *)" || textValue(mapping(p["FlexibleTimeWindow"])["Mode"]) != "OFF" || number(retry["MaximumEventAgeInSeconds"]) != 60 || number(retry["MaximumRetryAttempts"]) != 1 || mapping(target["DeadLetterConfig"])["Arn"] == nil {
				add("queue-schedule-policy", name+": schedule rate, retry, or DLQ differs")
			}
			group := targetGroup(mapping(target["SqsParameters"])["MessageGroupId"])
			if !strings.HasPrefix(group, "loc-") {
				add("queue-schedule-policy", name+": schedule group must start loc-")
			}
		case "AWS::Serverless::HttpApi":
			settings := mapping(p["DefaultRouteSettings"])
			if number(settings["ThrottlingBurstLimit"]) != 20 || number(settings["ThrottlingRateLimit"]) != 10 {
				add("runtime-policy", name+": HTTP API throttle differs")
			}
			if logs, ok := p["AccessLogSettings"]; ok && !ownedLog(mapping(logs)["DestinationArn"], resources) {
				add("runtime-policy", name+": HTTP access log must use owned log group")
			}
		}
	}
	if workerPresent && (!vehicleQueuePresent || queueVisibility < 6*workerTimeout || workerTimeout-20 < 90) {
		add("queue-schedule-policy", "worker timeout and visibility bounds fail")
	}
	if len(baseline) > 0 {
		old, err := parseTemplate(baseline)
		if err != nil {
			add("table-schema-policy", "invalid deployed schema baseline")
		} else {
			for name, raw := range mapping(old["Resources"]) {
				resource := mapping(raw)
				if textValue(resource["Type"]) == "AWS::DynamoDB::Table" {
					current := mapping(resources[name])
					if textValue(current["Type"]) != "AWS::DynamoDB::Table" || !reflect.DeepEqual(tableSchema(mapping(resource["Properties"])), tableSchema(mapping(current["Properties"]))) {
						add("table-schema-policy", name+": deployed table schema changed or table removed")
					}
				}
			}
		}
	}
	for _, name := range TemplateRequired {
		messages := issues[name]
		sort.Strings(messages)
		status := "pass"
		actual := "static assertions satisfied"
		if len(messages) > 0 {
			status = "fail"
			actual = strings.Join(messages, "; ")
		}
		r.Checks = append(r.Checks, Check{Name: name, Required: true, Measured: true, Status: status, Expected: "planned static infrastructure constraints", Actual: actual, Evidence: evidence})
	}
	status := "pass"
	actual := "worker configuration checked statically"
	if !workerPresent {
		status = "blocked"
		actual = "worker resource absent; full worker pipeline acceptance remains incomplete"
	}
	r.Checks = append(r.Checks, Check{Name: "worker-presence", Status: status, Measured: true, Actual: actual})
	if len(baseline) == 0 {
		r.Checks = append(r.Checks, Check{Name: "deployed-schema-baseline", Status: "skip", Measured: true, Actual: "no deployed baseline supplied; table schema freeze is not verified"})
	}
	return r
}

func parseTemplate(data []byte) (map[string]any, error) {
	var n yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&n); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if d.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("multiple documents")
	}
	v, err := yamlValue(&n)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("root must be mapping")
	}
	return m, nil
}
func yamlValue(n *yaml.Node) (any, error) {
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) != 1 {
			return nil, fmt.Errorf("invalid document")
		}
		return yamlValue(n.Content[0])
	}
	if n.Kind == yaml.AliasNode {
		return nil, fmt.Errorf("aliases are not supported")
	}
	var value any
	switch n.Kind {
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if _, ok := m[k]; ok {
				return nil, fmt.Errorf("duplicate key")
			}
			v, e := yamlValue(n.Content[i+1])
			if e != nil {
				return nil, e
			}
			m[k] = v
		}
		value = m
	case yaml.SequenceNode:
		s := []any{}
		for _, child := range n.Content {
			v, e := yamlValue(child)
			if e != nil {
				return nil, e
			}
			s = append(s, v)
		}
		value = s
	case yaml.ScalarNode:
		copy := *n
		if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
			copy.Tag = "!!str"
		}
		if err := copy.Decode(&value); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported YAML node")
	}
	if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
		tag := strings.TrimPrefix(n.Tag, "!")
		if tag != "Ref" {
			tag = "Fn::" + tag
		}
		return map[string]any{tag: value}, nil
	}
	return value, nil
}
func mapping(v any) map[string]any { m, _ := v.(map[string]any); return m }
func sequence(v any) []any         { s, _ := v.([]any); return s }
func textValue(v any) string       { s, _ := v.(string); return s }
func number(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	case uint64:
		return int(n)
	}
	return -1
}
func walkTemplate(v any, visit func(string, any)) {
	switch node := v.(type) {
	case map[string]any:
		for k, child := range node {
			visit(k, child)
			walkTemplate(child, visit)
		}
	case []any:
		for _, child := range node {
			walkTemplate(child, visit)
		}
	}
}
func hasNoncurrentExpiry(p map[string]any) bool {
	for _, raw := range sequence(p["Rules"]) {
		rule := mapping(raw)
		if textValue(rule["Status"]) == "Enabled" && number(mapping(rule["NoncurrentVersionExpiration"])["NoncurrentDays"]) > 0 {
			return true
		}
	}
	return false
}
func ownedLog(v any, resources map[string]any) bool {
	refs := map[string]bool{}
	walkTemplate(v, func(k string, val any) {
		if k == "Ref" {
			refs[textValue(val)] = true
		}
		if k == "Fn::GetAtt" {
			if s, ok := val.(string); ok {
				refs[strings.Split(s, ".")[0]] = true
			}
			if s := sequence(val); len(s) > 0 {
				refs[textValue(s[0])] = true
			}
		}
	})
	for name := range refs {
		r := mapping(resources[name])
		if textValue(r["Type"]) == "AWS::Logs::LogGroup" && number(mapping(r["Properties"])["RetentionInDays"]) == 7 {
			return true
		}
	}
	return false
}
func targetGroup(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return textValue(mapping(v)["Fn::Sub"])
}
func inspectPolicies(v any, name string, add func(string, string)) {
	policies := sequence(v)
	if policies == nil {
		policies = []any{v}
	}
	for _, policy := range policies {
		m := mapping(policy)
		if m["Statement"] == nil {
			add("resource-policy", name+": SAM policy templates and managed policies forbidden")
		} else {
			inspectPolicyDocument(m, name, add)
		}
	}
}
func inspectPolicyDocument(v any, name string, add func(string, string)) {
	doc := mapping(v)
	statements := sequence(doc["Statement"])
	if statements == nil && doc["Statement"] != nil {
		statements = []any{doc["Statement"]}
	}
	for _, raw := range statements {
		s := mapping(raw)
		if textValue(s["Effect"]) != "Allow" {
			continue
		}
		if s["NotAction"] != nil || s["NotResource"] != nil {
			add("resource-policy", name+": IAM complements forbidden")
		}
		actions := sequence(s["Action"])
		if actions == nil {
			actions = []any{s["Action"]}
		}
		for _, rawAction := range actions {
			action := strings.ToLower(textValue(rawAction))
			if action == "" || strings.ContainsAny(action, "*?") || action == "dynamodb:scan" || action == "logs:createloggroup" || action == "lambda:getfunction" || action == "lambda:getfunctionconfiguration" {
				add("resource-policy", name+": forbidden IAM action")
			}
			if (action == "sqs:receivemessage" || action == "sqs:deletemessage" || action == "sqs:changemessagevisibility") && !strings.Contains(strings.ToLower(name), "worker") {
				add("resource-policy", name+": queue consumption is worker-only")
			}
		}
		if s["Resource"] == "*" || s["Resource"] == nil {
			add("resource-policy", name+": explicit resource ARNs required")
		}
		for _, resource := range sequence(s["Resource"]) {
			if resource == "*" {
				add("resource-policy", name+": wildcard resource forbidden")
			}
		}
	}
}
func checkEvent(p map[string]any, name string, add func(string, string)) {
	if number(p["BatchSize"]) != 1 || number(mapping(p["ScalingConfig"])["MaximumConcurrency"]) != 2 {
		add("queue-schedule-policy", name+": worker event batch or concurrency differs")
	}
	if value, ok := p["MaximumBatchingWindowInSeconds"]; ok && number(value) != 0 {
		add("queue-schedule-policy", name+": batching window forbidden")
	}
}
func checkSchedulerTrust(p map[string]any, name string, add func(string, string)) {
	ok := false
	for _, raw := range sequence(mapping(p["AssumeRolePolicyDocument"])["Statement"]) {
		s := mapping(raw)
		if textValue(mapping(s["Principal"])["Service"]) == "scheduler.amazonaws.com" {
			condition := mapping(s["Condition"])
			equal := mapping(condition["StringEquals"])
			arn := mapping(condition["ArnLike"])["aws:SourceArn"]
			if arn == nil {
				arn = mapping(condition["ArnEquals"])["aws:SourceArn"]
			}
			account := textValue(equal["aws:SourceAccount"])
			expectedArn := map[string]any{"Fn::Sub": "arn:${AWS::Partition}:scheduler:${AWS::Region}:${AWS::AccountId}:schedule-group/default"}
			ok = textValue(s["Effect"]) == "Allow" && textValue(s["Action"]) == "sts:AssumeRole" && (account == "759775734231" || reflect.DeepEqual(equal["aws:SourceAccount"], map[string]any{"Ref": "AWS::AccountId"})) && reflect.DeepEqual(arn, expectedArn)
		} else if textValue(s["Effect"]) == "Allow" {
			add("queue-schedule-policy", name+": scheduler trust has an extra principal")
		}
	}
	if !ok {
		add("queue-schedule-policy", name+": scheduler trust must restrict account and default schedule-group ARN")
	}
}
func tableSchema(p map[string]any) string {
	canonical := func(value any) []string {
		var items []string
		for _, item := range sequence(value) {
			data, _ := json.Marshal(item)
			items = append(items, string(data))
		}
		sort.Strings(items)
		return items
	}
	data, _ := json.Marshal(map[string]any{"KeySchema": canonical(p["KeySchema"]), "AttributeDefinitions": canonical(p["AttributeDefinitions"])})
	return string(data)
}

func checkQueuePrincipals(v any, resources map[string]any, name string, add func(string, string)) {
	for _, raw := range sequence(mapping(v)["Statement"]) {
		s := mapping(raw)
		if textValue(s["Effect"]) != "Allow" {
			continue
		}
		principals := mapping(s["Principal"])["AWS"]
		list := sequence(principals)
		if list == nil {
			list = []any{principals}
		}
		for _, principal := range list {
			target := ""
			att := mapping(principal)["Fn::GetAtt"]
			if seq := sequence(att); len(seq) > 0 {
				target = textValue(seq[0])
			} else {
				target = strings.Split(textValue(att), ".")[0]
			}
			role := mapping(resources[target])
			lower := strings.ToLower(target)
			if textValue(role["Type"]) != "AWS::IAM::Role" || (!strings.Contains(lower, "scheduler") && !strings.Contains(lower, "api") && !strings.Contains(lower, "alexa") && target != "WorkerRole") {
				add("queue-schedule-policy", name+": queue sender principal is not an owned approved role")
			}
		}
	}
}
