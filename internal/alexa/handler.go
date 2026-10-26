// Package alexa validates Alexa requests and delegates authorized command work
// to the IAM-only worker. It never contacts the vehicle or logs request bodies.
package alexa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/JeremyProffittOrg/suburban-remote/pkg/contract"
)

type Application struct {
	ID string `json:"applicationId"`
}
type User struct {
	ID          string `json:"userId"`
	AccessToken string `json:"accessToken,omitempty"`
}
type Person struct {
	ID          string `json:"personId"`
	AccessToken string `json:"accessToken"`
}
type Envelope struct {
	Version string `json:"version"`
	Session *struct {
		Application Application `json:"application"`
		User        User        `json:"user"`
	} `json:"session,omitempty"`
	Context struct {
		System struct {
			Application Application `json:"application"`
			User        User        `json:"user"`
			Person      *Person     `json:"person,omitempty"`
		} `json:"System"`
	} `json:"context"`
	Request struct {
		Type                string `json:"type"`
		ID                  string `json:"requestId"`
		Timestamp           string `json:"timestamp"`
		EventPublishingTime string `json:"eventPublishingTime"`
		Intent              struct {
			Name  string          `json:"name"`
			Slots map[string]Slot `json:"slots,omitempty"`
		} `json:"intent"`
	} `json:"request"`
}
type Speech struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type Response struct {
	Version  string `json:"version"`
	Response struct {
		OutputSpeech *Speech `json:"outputSpeech,omitempty"`
		Card         *struct {
			Type string `json:"type"`
		} `json:"card,omitempty"`
		ShouldEndSession *bool `json:"shouldEndSession,omitempty"`
		Reprompt         *struct {
			OutputSpeech Speech `json:"outputSpeech"`
		} `json:"reprompt,omitempty"`
	} `json:"response"`
}
type Broker interface {
	Call(context.Context, contract.AlexaCommandRequest) (contract.AlexaCommandResponse, error)
}
type Handler struct {
	SkillID string
	Broker  Broker
	Now     func() time.Time
}

var requestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,256}$`)

func identifier(value string) bool {
	return value != "" && len(value) <= 2048 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\t\x00")
}
func fresh(value string, now time.Time) bool {
	stamp, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !stamp.Before(now.Add(-150*time.Second)) && !stamp.After(now.Add(30*time.Second))
}
func decode(raw json.RawMessage) (Envelope, error) {
	var envelope Envelope
	if len(raw) == 0 || len(raw) > 256*1024 {
		return envelope, errors.New("invalid Alexa envelope")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return envelope, errors.New("invalid Alexa envelope")
	}
	for name := range fields {
		switch name {
		case "version", "session", "context", "request":
		default:
			return envelope, errors.New("unexpected Alexa envelope field")
		}
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return envelope, errors.New("invalid Alexa envelope")
	}
	return envelope, nil
}
func say(text string, end bool) Response {
	out := Response{Version: "1.0"}
	out.Response.OutputSpeech = &Speech{Type: "PlainText", Text: text}
	out.Response.ShouldEndSession = &end
	if !end {
		out.Response.Reprompt = &struct {
			OutputSpeech Speech `json:"outputSpeech"`
		}{Speech{Type: "PlainText", Text: "Say status, lock, or unlock."}}
	}
	return out
}
func empty() Response { return Response{Version: "1.0"} }
func (h Handler) Handle(ctx context.Context, raw json.RawMessage) (Response, error) {
	envelope, err := decode(raw)
	if err != nil {
		return empty(), err
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	system := envelope.Context.System
	if (envelope.Version != "1.0" && envelope.Version != "1.1") || h.SkillID == "" || system.Application.ID != h.SkillID || !requestID.MatchString(envelope.Request.ID) {
		return empty(), errors.New("Alexa request rejected")
	}
	if envelope.Session != nil && (envelope.Session.Application.ID != h.SkillID || (envelope.Session.User.ID != "" && envelope.Session.User.ID != system.User.ID)) {
		return empty(), errors.New("Alexa session binding rejected")
	}
	event := false
	switch envelope.Request.Type {
	case "AlexaSkillEvent.SkillDisabled", "AlexaSkillEvent.SkillAccountLinked", "AlexaSkillEvent.SkillAccountUnlinked":
		event = true
	}
	timestamp := envelope.Request.Timestamp
	if event {
		log.Print("alexa_request operation=event")
		timestamp = envelope.Request.EventPublishingTime
	}
	if !fresh(timestamp, now) {
		return empty(), errors.New("Alexa request timestamp rejected")
	}
	budget := 4500 * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) - 250*time.Millisecond; remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		return empty(), errors.New("Alexa response budget exhausted")
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	input := contract.AlexaCommandRequest{Kind: "alexa-command", Source: "alexa", SkillID: h.SkillID, RequestID: envelope.Request.ID, Timestamp: timestamp, UserID: system.User.ID}
	if event {
		if !identifier(system.User.ID) {
			return empty(), errors.New("Alexa event identity rejected")
		}
		input.Operation = "event"
		input.EventType = envelope.Request.Type
		input.EventPublishingTime = envelope.Request.EventPublishingTime
		if h.Broker == nil {
			return empty(), errors.New("Alexa broker unavailable")
		}
		eventResult, eventErr := h.Broker.Call(ctx, input)
		if eventErr != nil || eventResult.State != "succeeded" {
			return empty(), errors.New("Alexa binding event persistence failed")
		}
		return empty(), nil
	}
	switch envelope.Request.Type {
	case "SessionEndedRequest":
		return empty(), nil
	case "LaunchRequest":
		log.Print("alexa_request operation=launch")
		return say("Suburban. Say status, lock, or unlock.", false), nil
	case "IntentRequest":
		switch envelope.Request.Intent.Name {
		case "AMAZON.StopIntent", "AMAZON.CancelIntent":
			return say("Goodbye.", true), nil
		case "AMAZON.HelpIntent", "AMAZON.NavigateHomeIntent":
			return say("Say status for fuel, range, and mileage, lock or unlock for the doors, or command status for the last request. Say feature catalog for other options.", false), nil
		case "FeatureCatalogIntent":
			return say(fmt.Sprintf("The catalog has %d features: engine, doors, cargo, alerts, health, garage, plans, recalls, warranty, radio, location, and EV previews. Live support must be verified.", len(contract.FeatureCatalog())), false), nil
		case "PreviewFeatureIntent":
			return say(previewFeature(envelope.Request.Intent.Slots), true), nil
		case "ConnectionStatusIntent":
			input.Operation = "features"
		case "CommandStatusIntent":
			input.Operation = "status"
		case "VehicleStatusIntent":
			input.Operation = "read"
			input.FeatureID = "diagnostics"
		default:
			id, exists := featureIntents[envelope.Request.Intent.Name]
			feature, known := contract.FindFeature(id)
			if !exists || !known {
				return say("I did not recognize that request. Say feature catalog or preview a feature.", false), nil
			}
			switch feature.Category {
			case "action":
				input.Operation = "submit"
				input.Action = feature.Action
				input.Options, err = parseOptions(feature.Action, envelope.Request.Intent.Slots)
				if err != nil {
					return say("Those command parameters are invalid. Use whole numbers, a nonnegative delay, a positive duration, and supported horn or light actions.", true), nil
				}
			case "read":
				input.Operation = "read"
				input.FeatureID = feature.ReadID
			case "ev":
				if feature.ID == "ev-set-charge-target" {
					if value, parseErr := slotValue(envelope.Request.Intent.Slots["Target"]); parseErr != nil || !chargeTargetValid(value) {
						return say("The example charge target must be a whole percentage from one to one hundred. Live EV control is unavailable.", true), nil
					}
				}
				return say("Live EV support and its separate authorization are unverified for this vehicle. You can explicitly ask for a preview of this feature.", true), nil
			default:
				return say("That feature is unavailable.", true), nil
			}
		}
	default:
		return say("That request is not supported.", true), nil
	}
	// Use the linked Amazon account regardless of speaker recognition. The worker
	// still freshly validates this token against the configured owner.
	personPresent := system.Person != nil && identifier(system.Person.ID)
	personLinked := personPresent && strings.TrimSpace(system.Person.AccessToken) != "" && len(system.Person.AccessToken) <= 16384
	accountLinked := strings.TrimSpace(system.User.AccessToken) != "" && len(system.User.AccessToken) <= 16384
	log.Printf("alexa_auth operation=%s user_present=%t account_token_present=%t person_present=%t person_token_present=%t", input.Operation, identifier(system.User.ID), accountLinked, personPresent, personLinked)
	if !identifier(system.User.ID) {
		return say("Alexa did not supply a valid account identity. Please try again.", true), nil
	}
	if !accountLinked {
		out := say("Link your owner account to Suburban in the Alexa app, then try again.", true)
		out.Response.Card = &struct {
			Type string `json:"type"`
		}{Type: "LinkAccount"}
		return out, nil
	}
	// Preserve the existing broker/storage contract's identity field names while
	// binding Alexa requests to the shared account, not a voice profile.
	input.PersonID = system.User.ID
	input.PersonAccessToken = system.User.AccessToken
	if h.Broker == nil {
		return say("I could not verify your request right now. Check the app before trying again.", true), nil
	}
	result, err := h.Broker.Call(ctx, input)
	if err != nil {
		log.Printf("alexa_broker operation=%s outcome=unavailable", input.Operation)
		if input.Operation != "submit" {
			return say("I could not retrieve that information right now. Check the app before trying again.", true), nil
		}
		return say("I could not confirm whether the request was queued. Ask for command status before trying again.", true), nil
	}
	reason := "other"
	switch result.Reason {
	case "", "person-link-required", "owner-authority-unverified", "authority-unavailable", "owner-binding-unverified", "binding-revoked", "storage-unavailable", "invalid-request", "reauth-required", "unlinked", "owner-mismatch", "session-revoked":
		reason = result.Reason
	}
	log.Printf("alexa_broker operation=%s succeeded=%t reason=%s", input.Operation, result.State == "succeeded", reason)
	if input.Operation == "submit" {
		switch result.State {
		case "queued", "dispatching", "submitted", "succeeded":
			if action := map[string]string{"lock": "Lock", "unlock": "Unlock", "start": "Start"}[input.Action]; action != "" {
				return say(action+" command sent.", true), nil
			}
		}
	}
	if envelope.Request.Intent.Name == "VehicleStatusIntent" && result.State == "succeeded" {
		return say(vehicleStatusSpeech(result.Snapshot, now), true), nil
	}
	if input.Operation == "read" && result.Snapshot != nil {
		return say(snapshotSpeech(result.Snapshot, input.FeatureID, now), true), nil
	}
	if input.Operation == "features" && result.State == "succeeded" {
		return say(availabilitySpeech(result.Availability), true), nil
	}
	return say(commandSpeech(result, input.Operation == "status", now), true), nil
}
func commandSpeech(result contract.AlexaCommandResponse, status bool, now time.Time) string {
	action := "command"
	if feature, ok := contract.FindFeature(result.Action); ok && feature.Category == "action" {
		action = strings.ToLower(feature.Title) + " command"
	}
	var text string
	switch result.State {
	case "accepted", "queued":
		text = "The request is queued. Ask Suburban for command status shortly."
	case "unconfirmed":
		text = "I could not confirm whether the request was queued. Ask for command status before trying again."
	case "dispatching", "submitted":
		text = "The command is still pending. Ask Suburban for command status shortly."
	case "succeeded":
		text = "The vehicle confirmed the " + action + "."
	case "outcome-unknown":
		text = "I do not know whether the command completed. Check the app or vehicle before trying again."
	case "expired":
		text = "The request expired before dispatch. Check the app before trying again."
	case "not-found":
		text = "There is no recent command for your linked account."
	case "failed", "rejected":
		switch result.Reason {
		case "reauth-required", "unlinked", "owner-mismatch", "session-revoked", "binding-revoked":
			text = "Your owner account needs to be linked again. Open Suburban in the Alexa app."
		case "provider-cooldown":
			text = "GM access is temporarily paused. Check the app for the cooldown time."
		case "actuation-disabled", "controls-disabled", "paused", "provider-paused":
			text = "Vehicle controls are paused. Check the app before trying again."
		case "vehicle-not-linked":
			text = "GM did not link this vehicle to the owner account. Check your myChevrolet garage."
		case "capability-unverified":
			text = "This vehicle command has not been verified yet. Check the app."
		case "owner-authority-unverified":
			text = "I could not verify your owner account right now. Check the app before trying again."
		case "unsupported", "not-entitled", "subscription-lapsed":
			text = "This command is not available for the vehicle. Check your OnStar service."
		case "replay", "duplicate":
			text = "That request was already handled. Ask Suburban for command status."
		default:
			text = "The command was not completed. Check the app for its status."
		}
	default:
		text = "I could not verify the command status. Check the app."
	}
	if status && result.UpdatedAt > 0 && result.UpdatedAt <= now.Unix() {
		age := now.Unix() - result.UpdatedAt
		if age < 60 {
			text += fmt.Sprintf(" Updated %d seconds ago.", age)
		} else {
			text += fmt.Sprintf(" Updated %d minutes ago.", age/60)
		}
	}
	return text
}
