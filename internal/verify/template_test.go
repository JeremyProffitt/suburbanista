package verify

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func templateFixture(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("../../cmd/verify/testdata/template/good.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestPrivateMapStorageBoundary(t *testing.T) {
	data, err := os.ReadFile("../../template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseTemplate(data)
	if err != nil {
		t.Fatal(err)
	}
	resources := mapping(doc["Resources"])
	if _, exists := resources["MapBucketPolicy"]; exists {
		t.Fatal("obsolete public map policy remains")
	}
	block := mapping(mapping(mapping(resources["MapBucket"])["Properties"])["PublicAccessBlockConfiguration"])
	for _, name := range []string{"BlockPublicAcls", "IgnorePublicAcls", "BlockPublicPolicy", "RestrictPublicBuckets"} {
		if block[name] != true {
			t.Fatalf("%s not enabled", name)
		}
	}
	for _, tc := range []struct{ role, action string }{{"ApiRole", "s3:GetObject"}, {"MapResolverRole", "s3:PutObject"}} {
		found := false
		for _, policy := range sequence(mapping(mapping(resources[tc.role])["Properties"])["Policies"]) {
			for _, statement := range sequence(mapping(mapping(policy)["PolicyDocument"])["Statement"]) {
				s := mapping(statement)
				resource, _ := json.Marshal(s["Resource"])
				if strings.Contains(string(resource), "MapBucket") {
					if tc.role == "MapResolverRole" && textValue(s["Action"]) == "s3:ListBucket" {
						continue
					}
					actions, _ := json.Marshal(s["Action"])
					if !strings.Contains(string(actions), tc.action) || !strings.Contains(string(resource), "/map-cache/*") {
						t.Fatal("map IAM scope too broad")
					}
					found = true
				}
			}
		}
		if !found {
			t.Fatal("required map IAM grant absent")
		}
	}
	worker, _ := json.Marshal(mapping(mapping(resources["WorkerRole"])["Properties"])["Policies"])
	if strings.Contains(string(worker), "MapBucket") || strings.Contains(string(worker), "geo-maps:") {
		t.Fatal("collector has map generation permissions")
	}
	resolverPolicies, _ := json.Marshal(mapping(mapping(resources["MapResolverRole"])["Properties"])["Policies"])
	for _, action := range []string{"geo-maps:GetTile", "geo-maps:GetStyleDescriptor"} {
		if !strings.Contains(string(resolverPolicies), action) {
			t.Fatal("raster map permission missing", action)
		}
	}
	if strings.Contains(string(resolverPolicies), "geo-maps:*") {
		t.Fatal("raster map grant too broad")
	}
	for _, policy := range sequence(mapping(mapping(resources["MapResolverRole"])["Properties"])["Policies"]) {
		for _, statement := range sequence(mapping(mapping(policy)["PolicyDocument"])["Statement"]) {
			s := mapping(statement)
			actions, _ := json.Marshal(s["Action"])
			if strings.Contains(string(actions), "geo-maps:GetTile") || strings.Contains(string(actions), "geo-maps:GetStyleDescriptor") {
				resource, _ := json.Marshal(s["Resource"])
				if !strings.Contains(string(resource), "::provider/default") || strings.Contains(string(resource), "*") {
					t.Fatal("raster map provider scope too broad")
				}
			}
		}
	}
	stream := mapping(mapping(mapping(resources["StateTable"])["Properties"])["StreamSpecification"])
	if textValue(stream["StreamViewType"]) != "NEW_AND_OLD_IMAGES" {
		t.Fatal("observation stream missing")
	}
	resolver := mapping(mapping(resources["MapResolverFunction"])["Properties"])
	event := mapping(mapping(mapping(mapping(resolver["Events"])["Observations"])["Properties"]))
	if number(event["MaximumRetryAttempts"]) != 2 || number(event["MaximumRecordAgeInSeconds"]) != 3600 || event["BisectBatchOnFunctionError"] != true || event["DestinationConfig"] == nil {
		t.Fatal("resolver recovery bounds missing")
	}
	filters, _ := json.Marshal(event["FilterCriteria"])
	if !strings.Contains(string(filters), "map-request#") || strings.Contains(string(filters), "location-observation#") || !strings.Contains(string(filters), "INSERT") {
		t.Fatal("stream filter missing")
	}
	if mapping(doc["Outputs"])["PublicMapURL"] != nil || mapping(doc["Outputs"])["MapURL"] == nil {
		t.Fatal("map output not authenticated endpoint")
	}
}

func TestHourlyTelemetryInfrastructure(t *testing.T) {
	data, err := os.ReadFile("../../template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseTemplate(data)
	if err != nil {
		t.Fatal(err)
	}
	resources := mapping(doc["Resources"])
	function := mapping(mapping(resources["HourlyTelemetryFunction"])["Properties"])
	if number(function["Timeout"]) != 90 || number(function["MemorySize"]) != 256 || number(function["ReservedConcurrentExecutions"]) != 1 || textValue(function["Runtime"]) != "provided.al2023" {
		t.Fatal("collector runtime bounds")
	}
	async := mapping(function["EventInvokeConfig"])
	if number(async["MaximumRetryAttempts"]) != 0 || number(async["MaximumEventAgeInSeconds"]) != 60 || mapping(async["DestinationConfig"])["OnFailure"] == nil {
		t.Fatal("collector async failure bounds")
	}
	variables := mapping(mapping(function["Environment"])["Variables"])
	for _, key := range []string{"STATE_TABLE", "OWNER_AMAZON_USER_ID", "CREDENTIAL_ENCRYPTION_KEY_A", "CREDENTIAL_ENCRYPTION_KEY_B", "CREDENTIAL_ENCRYPTION_KEY_ACTIVE", "ONSTAR_DEVICE_ID", "VEHICLE_KEY", "TELEMETRY_SCHEDULE_ARN"} {
		if variables[key] == nil {
			t.Fatal("collector binding missing", key)
		}
	}
	for _, key := range []string{"LWA_CLIENT_SECRET", "WORK_QUEUE_URL", "MAP_BUCKET", "MAP_READ_KEY_SHA256"} {
		if variables[key] != nil {
			t.Fatal("unnecessary collector authority", key)
		}
	}
	for _, raw := range sequence(mapping(mapping(resources["HourlyTelemetryRole"])["Properties"])["Policies"]) {
		for _, statement := range sequence(mapping(mapping(raw)["PolicyDocument"])["Statement"]) {
			row := mapping(statement)
			resource, _ := json.Marshal(row["Resource"])
			actions := sequence(row["Action"])
			if actions == nil {
				actions = []any{row["Action"]}
			}
			for _, item := range actions {
				action := textValue(item)
				switch action {
				case "logs:CreateLogStream", "logs:PutLogEvents":
					if !strings.Contains(string(resource), "HourlyTelemetryLogGroup") {
						t.Fatal("collector log scope")
					}
				case "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem":
					if !strings.Contains(string(resource), "StateTable") {
						t.Fatal("collector table scope")
					}
				case "sqs:SendMessage":
					if !strings.Contains(string(resource), "SchedulerDLQ") {
						t.Fatal("collector may not dispatch commands")
					}
				default:
					t.Fatal("unexpected collector permission", action)
				}
			}
		}
	}
	schedule := mapping(mapping(resources["HourlyTelemetrySchedule"])["Properties"])
	input := textValue(mapping(mapping(schedule["Target"])["Input"])["Fn::Sub"])
	if !strings.Contains(input, "hourly-diagnostics") || !strings.Contains(input, "<aws.scheduler.scheduled-time>") {
		t.Fatal("collector event contract")
	}
	for _, mutate := range []func(){func() { schedule["ScheduleExpression"] = "rate(5 minutes)" }, func() { schedule["State"] = "DISABLED" }, func() { mapping(mapping(schedule["Target"])["RetryPolicy"])["MaximumRetryAttempts"] = 1 }} {
		original, _ := json.Marshal(doc)
		mutate()
		invalid, _ := json.Marshal(doc)
		if Template(invalid, "fixture", nil, strings.Repeat("a", 40), "test", time.Now()).Validate(TemplateRequired) == nil {
			t.Fatal("invalid hourly schedule accepted")
		}
		if json.Unmarshal(original, &doc) != nil {
			t.Fatal("fixture reset")
		}
		resources = mapping(doc["Resources"])
		schedule = mapping(mapping(resources["HourlyTelemetrySchedule"])["Properties"])
	}
}

func TestHourlyTelemetryPackagedIntrinsicShapes(t *testing.T) {
	data, err := os.ReadFile("../../template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseTemplate(data)
	if err != nil {
		t.Fatal(err)
	}
	resources := mapping(doc["Resources"])
	target := mapping(mapping(mapping(resources["HourlyTelemetrySchedule"])["Properties"])["Target"])
	target["Arn"] = map[string]any{"Fn::GetAtt": []any{"HourlyTelemetryFunction", "Arn"}}
	target["RoleArn"] = map[string]any{"Fn::GetAtt": []any{"HourlyTelemetrySchedulerRole", "Arn"}}
	packaged, _ := json.Marshal(doc)
	if Template(packaged, "packaged", nil, strings.Repeat("a", 40), "test", time.Now()).Validate(TemplateRequired) != nil {
		t.Fatal("equivalent packaged intrinsic was rejected")
	}
	target["Arn"] = map[string]any{"Fn::GetAtt": []any{"WorkerFunction", "Arn"}}
	wrong, _ := json.Marshal(doc)
	if Template(wrong, "packaged", nil, strings.Repeat("a", 40), "test", time.Now()).Validate(TemplateRequired) == nil {
		t.Fatal("wrong packaged target was accepted")
	}
}
func TestTemplateRejectsUnsafeResources(t *testing.T) {
	for _, kind := range []string{"AWS::SecretsManager::Secret", "AWS::KMS::Key", "AWS::EC2::Instance", "AWS::AutoScaling::AutoScalingGroup", "AWS::CloudWatch::Alarm", "AWS::CloudWatch::Dashboard", "AWS::Serverless::Api", "AWS::Backup::BackupPlan"} {
		t.Run(kind, func(t *testing.T) {
			doc := templateFixture(t)
			mapping(doc["Resources"])["Unsafe"] = map[string]any{"Type": kind}
			data, _ := json.Marshal(doc)
			r := Template(data, "fixture", nil, strings.Repeat("a", 40), "test", time.Now())
			if r.Validate(TemplateRequired) == nil {
				t.Fatal("unsafe resource accepted")
			}
		})
	}
}

func TestCommandBrokerAndAlexaBoundaries(t *testing.T) {
	data, err := os.ReadFile("../../template.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if Template(data, "repository", nil, strings.Repeat("a", 40), "test", time.Now()).Validate(TemplateRequired) != nil {
		t.Fatal("owned worker sender and exact Alexa trigger should pass")
	}
	for _, mutation := range []string{"missing-skill", "wildcard-skill", "alexa-secret", "unowned-worker"} {
		t.Run(mutation, func(t *testing.T) {
			doc, err := parseTemplate(data)
			if err != nil {
				t.Fatal(err)
			}
			resources := mapping(doc["Resources"])
			properties := mapping(mapping(resources["AlexaFunction"])["Properties"])
			trigger := mapping(mapping(mapping(properties["Events"])["AlexaTrigger"])["Properties"])
			switch mutation {
			case "missing-skill":
				delete(trigger, "SkillId")
			case "wildcard-skill":
				trigger["SkillId"] = "*"
			case "alexa-secret":
				mapping(mapping(properties["Environment"])["Variables"])["LWA_CLIENT_SECRET"] = "synthetic-private-value"
			case "unowned-worker":
				delete(resources, "WorkerRole")
			}
			changed, _ := json.Marshal(doc)
			if Template(changed, "fixture", nil, strings.Repeat("a", 40), "test", time.Now()).Validate(TemplateRequired) == nil {
				t.Fatal("unsafe command boundary accepted")
			}
		})
	}
}
func TestTemplatePolicyFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"scan", func(doc map[string]any) {
			mapping(doc["Resources"])["Policy"] = map[string]any{"Type": "AWS::IAM::Policy", "Properties": map[string]any{"PolicyDocument": map[string]any{"Statement": []any{map[string]any{"Effect": "Allow", "Action": "dynamodb:Scan", "Resource": "table"}}}}}
		}},
		{"vpc", func(doc map[string]any) { doc["VpcConfig"] = map[string]any{} }},
		{"wildcard", func(doc map[string]any) {
			mapping(doc["Resources"])["Policy"] = map[string]any{"Type": "AWS::IAM::Policy", "Properties": map[string]any{"PolicyDocument": map[string]any{"Statement": []any{map[string]any{"Effect": "Allow", "Action": "dynamodb:*", "Resource": "table"}}}}}
		}},
		{"concurrency", func(doc map[string]any) {
			delete(mapping(mapping(mapping(doc["Resources"])["ApiFunction"])["Properties"]), "ReservedConcurrentExecutions")
		}},
		{"api-timeout", func(doc map[string]any) {
			mapping(mapping(mapping(doc["Resources"])["ApiFunction"])["Properties"])["Timeout"] = 10
		}},
		{"sam-policy", func(doc map[string]any) {
			mapping(mapping(mapping(doc["Resources"])["ApiFunction"])["Properties"])["Policies"] = []any{map[string]any{"DynamoDBCrudPolicy": map[string]any{"TableName": "table"}}}
		}},
		{"versioning", func(doc map[string]any) {
			mapping(doc["Resources"])["Bucket"] = map[string]any{"Type": "AWS::S3::Bucket", "Properties": map[string]any{"VersioningConfiguration": map[string]any{"Status": "Enabled"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := templateFixture(t)
			tc.change(doc)
			data, _ := json.Marshal(doc)
			if Template(data, "fixture", nil, strings.Repeat("a", 40), "test", time.Now()).Validate(TemplateRequired) == nil {
				t.Fatal("unsafe template passed")
			}
		})
	}
}
func TestTemplateParsingAndSchema(t *testing.T) {
	if _, err := parseTemplate([]byte("Resources: {}\nResources: {}\n")); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := parseTemplate([]byte("Resources: {}\n---\nResources: {}\n")); err == nil {
		t.Fatal("multiple documents accepted")
	}
	doc, err := parseTemplate([]byte("Resources:\n  Log:\n    Type: AWS::Logs::LogGroup\n    Properties:\n      RetentionInDays: 7\nValue: !GetAtt Log.Arn\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !ownedLog(doc["Value"], mapping(doc["Resources"])) {
		t.Fatal("intrinsic lost")
	}
	base := templateFixture(t)
	mapping(base["Resources"])["StateTable"] = map[string]any{"Type": "AWS::DynamoDB::Table", "Properties": map[string]any{"KeySchema": []any{map[string]any{"AttributeName": "pk", "KeyType": "HASH"}}}}
	baseline, _ := json.Marshal(base)
	current, _ := json.Marshal(templateFixture(t))
	r := Template(current, "fixture", baseline, strings.Repeat("a", 40), "test", time.Now())
	if r.Validate(TemplateRequired) == nil {
		t.Fatal("removed deployed table accepted")
	}
}

func TestTemplateQueueAndSchedulePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		properties map[string]any
	}{
		{"WorkerDLQ", "AWS::SQS::Queue", map[string]any{"QueueName": "bad", "MessageRetentionPeriod": 604800}},
		{"SchedulerDLQ", "AWS::SQS::Queue", map[string]any{"QueueName": "bad.fifo", "FifoQueue": true, "MessageRetentionPeriod": 604800}},
		{"WorkQueue", "AWS::SQS::Queue", map[string]any{"FifoQueue": true, "VisibilityTimeout": 100}},
		{"LocationSchedule", "AWS::Scheduler::Schedule", map[string]any{"ScheduleExpression": "rate(15 minutes)", "FlexibleTimeWindow": map[string]any{"Mode": "OFF"}, "Target": map[string]any{}}},
		{"WorkerMapping", "AWS::Lambda::EventSourceMapping", map[string]any{"BatchSize": 10}},
		{"QueuePolicy", "AWS::SQS::QueuePolicy", map[string]any{"PolicyDocument": map[string]any{"Statement": []any{map[string]any{"Effect": "Allow", "Principal": "*", "Action": "sqs:SendMessage", "Resource": "arn:aws:sqs:us-east-1:123456789012:queue"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := templateFixture(t)
			mapping(doc["Resources"])[tc.name] = map[string]any{"Type": tc.kind, "Properties": tc.properties}
			data, _ := json.Marshal(doc)
			if Template(data, "fixture", nil, strings.Repeat("a", 40), "test", time.Now()).Validate(TemplateRequired) == nil {
				t.Fatal("bad queue/schedule accepted")
			}
		})
	}
}
