package mobile

import "github.com/JeremyProffittOrg/suburban-remote/mobile/internal/ownerclient"

const ServiceURL = ownerclient.ServiceURL

type Client = ownerclient.Client
type Login = ownerclient.Login
type Session = ownerclient.Session
type Capability = ownerclient.Capability
type Dashboard = ownerclient.Dashboard
type PendingLogin = ownerclient.PendingLogin
type SavedState = ownerclient.SavedState
type CommandIntent = ownerclient.CommandIntent
type CommandResult = ownerclient.CommandResult
type CommandVerification = ownerclient.CommandVerification
type Vault = ownerclient.Vault
type readEnvelope = ownerclient.ReadEnvelope

var (
	ErrPending         = ownerclient.ErrPending
	ErrUnauthorized    = ownerclient.ErrUnauthorized
	NewClient          = ownerclient.NewClient
	NewVerifier        = ownerclient.NewVerifier
	WidgetCommand      = ownerclient.WidgetCommand
	appendVerification = ownerclient.AppendVerification
	verificationResult = ownerclient.VerificationResult
)
