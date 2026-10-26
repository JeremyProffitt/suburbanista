package onstar

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type HealthElement struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"displayName"`
	Status      string  `json:"status"`
	Value       *string `json:"value"`
	Unit        string  `json:"uom"`
	ObservedAt  string  `json:"cts"`
}
type HealthDiagnostic struct {
	HealthElement
	RecommendedAction string          `json:"recommendedAction"`
	Elements          []HealthElement `json:"diagnosticElements"`
}
type HealthStatus struct {
	HealthElement
	RecommendedAction string             `json:"recommendedAction"`
	Diagnostics       []HealthDiagnostic `json:"diagnostics"`
}
type VehicleCommand struct {
	Name             string          `json:"name"`
	Eligible         *bool           `json:"isEligible"`
	IneligibleReason json.RawMessage `json:"inEligibleReason"`
}
type VehicleDetails struct {
	Make          string           `json:"make"`
	Model         string           `json:"model"`
	Year          json.RawMessage  `json:"year"`
	OnstarCapable *bool            `json:"onstarCapable"`
	Commands      []VehicleCommand `json:"vehicleCommands"`
	Color         struct {
		Exterior string `json:"exteriorColor"`
		Interior string `json:"interiorTrimColor"`
	} `json:"color"`
	Metadata struct {
		Fuel struct {
			Category   string `json:"fuelCategory"`
			Propulsion string `json:"propulsionType"`
		} `json:"propulsionAndFuelType"`
		Country   string `json:"configuredCountry"`
		BodyStyle string `json:"bodyStyle"`
	} `json:"vehicleMetaData"`
	OnstarInfo struct {
		Status string `json:"onStarStatus"`
		Shared *bool  `json:"isShared"`
	} `json:"onstarInfo"`
}
type PlanItem struct {
	Name       string `json:"planName"`
	Code       string `json:"productCode"`
	Status     string `json:"status"`
	StartDate  string `json:"startDate"`
	EndDate    string `json:"endDate"`
	ExpiryDate string `json:"expiryDate"`
	Trial      *bool  `json:"isTrial"`
	Cadence    string `json:"billingCadence"`
	Features   []struct {
		Name string `json:"featureName"`
	} `json:"features"`
}
type Offer struct {
	Name    string          `json:"offerName"`
	Price   json.RawMessage `json:"retailPrice"`
	Cadence string          `json:"billingCadence"`
}
type OnstarPlan struct {
	Make    string          `json:"make"`
	Model   string          `json:"model"`
	Year    json.RawMessage `json:"year"`
	Expiry  []PlanItem      `json:"planExpiryInfo"`
	Plans   []PlanItem      `json:"planInfo"`
	Active  []PlanItem      `json:"activePlans"`
	Offers  []Offer         `json:"offers"`
	Partial bool            `json:"-"`
}
type Recall struct {
	Title        string `json:"title"`
	Type         string `json:"typeDescription"`
	Description  string `json:"description"`
	Status       string `json:"recallStatus"`
	RepairStatus string `json:"repairStatus"`
	Repair       string `json:"repairDescription"`
	Safety       string `json:"safetyRiskDescription"`
	Completed    string `json:"completedDate"`
}
type Warranty struct {
	StartMileage json.RawMessage `json:"startMileage"`
	EndMileage   json.RawMessage `json:"endMileage"`
	Start        string          `json:"startDate"`
	Expires      string          `json:"expirationDate"`
	Type         string          `json:"typeDescription"`
	Status       string          `json:"status"`
	Unit         string          `json:"mileageUnit"`
	Notes        string          `json:"notes"`
}
type SxmSubscription struct {
	Name     string   `json:"subscriptionName"`
	Start    string   `json:"startDate"`
	End      string   `json:"endDate"`
	Services []string `json:"services"`
}
type SxmInfo struct {
	Subscriptions    []SxmSubscription `json:"subscriptions"`
	Suspended        *bool             `json:"suspended"`
	Status           string            `json:"status"`
	DeactivationDate string            `json:"audioDeactDate"`
}

func (c *Client) Diagnostics(ctx context.Context, token GMToken, vin string) (HealthStatus, error) {
	var out HealthStatus
	if e := c.commandToken(token, vin); e != nil {
		return out, e
	}
	e := c.request(ctx, http.MethodGet, APIBase+"/api/v1/vh/vehiclehealth/v1/healthstatus/"+vin, "application/json", token.AccessToken, nil, &out)
	return out, e
}

// These static selections reproduce pinned RequestService.ts. The VIN is
// validated before formatting, and provider bodies never enter logs or errors.
const detailsFields = `vin make model year onstarCapable imageUrl rpoCodes orderDate permissions { userPermissions accountPermissions { acctNum permissions disabledPermissionsResponse { code reason } } devicePermissions { id permissions disabledPermissions { code reason } } } vehicleCommands { name url serviceId isEligible inEligibleReason metaData { supportedDiagnostics } } color { exteriorColor interiorTrimColor } vehicleMetaData { propulsionAndFuelType { fuelCategory propulsionType } unit { unitGen unitGenDescription } configuredCountry bodyStyle features extColor } onstarInfo { onStarStatus ownerAccount isShared associatedDate }`
const planFields = `model make year onstarInfo { onStarStatus ownerAccount isShared associatedDate } offers { productCode offerName associatedOfferingCode retailPrice billingCadence productRank discounts { name discountCategory price duration { uom value } } } activePlans { offeringId isTrial orderNumber productId billingCadence startDate endDate } orders { offeringId trial status salesOrderType salesProducts { salesProductType orderNumber salesProductId billingCadence startDate endDate } } planExpiryInfo { productCode planName startDate endDate expiryDate orderDate isTrial type billingCadence cancelDate status features { featureCode featureName priorityNumber featureCategoryCode } additionalInfo { radioId } } planInfo { productCode billingCadence status startDate endDate expiryDate cancelDate orderDate pricePlan productType isTrial orderItemTags offers { offerName associatedOfferingCode retailPrice billingCadence productRank discounts { name discountCategory price duration { uom value } } } }`
const recallFields = `recallInfo { recallId title type typeDescription description recallStatus repairStatus repairStatusCode repairDescription safetyRiskDescription completedDate }`
const warrantyFields = `warrantyInfo { startMileage endMileage startDate expirationDate typeDescription status mileageUnit notes }`
const sxmFields = `sxmSubscriptionInfo { deviceId subscriptions { subscriptionName packageId marketType startDate endDate services } channelAccountInfo { message radioId marketType packageName expiryDate phoneNumber } is360Device audioDeactDate audioDeactType suspended status }`

func (c *Client) garageDetails(ctx context.Context, token GMToken, vin, name, fields string, partialAllowed bool, dst any) (bool, error) {
	if e := c.commandToken(token, vin); e != nil {
		return false, e
	}
	query := fmt.Sprintf("query %s {vehicleDetails(vin: %q) {%s }}", name, vin, fields)
	var response struct {
		Data struct {
			Details json.RawMessage `json:"vehicleDetails"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if e := c.request(ctx, http.MethodPost, APIBase+"/mbff/garage/v1", "text/plain; charset=utf-8", token.AccessToken, strings.NewReader(query), &response); e != nil {
		return false, e
	}
	partial := len(response.Errors) > 0
	if partial && !partialAllowed {
		return false, failure("garage-query-failed")
	}
	if len(response.Data.Details) == 0 || string(response.Data.Details) == "null" {
		return false, failure("vehicle-data-unavailable")
	}
	if json.Unmarshal(response.Data.Details, dst) != nil {
		return false, failure("invalid-vehicle-data")
	}
	return partial, nil
}
func (c *Client) VehicleDetails(ctx context.Context, t GMToken, vin string) (VehicleDetails, error) {
	var out VehicleDetails
	_, e := c.garageDetails(ctx, t, vin, "getMbffGarageVehicleDetails", detailsFields, false, &out)
	return out, e
}
func (c *Client) OnstarPlan(ctx context.Context, t GMToken, vin string) (OnstarPlan, error) {
	var out OnstarPlan
	partial, e := c.garageDetails(ctx, t, vin, "getPlanInfo", planFields, true, &out)
	out.Partial = partial
	return out, e
}
func (c *Client) Recalls(ctx context.Context, t GMToken, vin string) ([]Recall, error) {
	var out struct {
		Items []Recall `json:"recallInfo"`
	}
	_, e := c.garageDetails(ctx, t, vin, "getVehicleRecallInfo", recallFields, false, &out)
	return out.Items, e
}
func (c *Client) Warranty(ctx context.Context, t GMToken, vin string) ([]Warranty, error) {
	var out struct {
		Items []Warranty `json:"warrantyInfo"`
	}
	_, e := c.garageDetails(ctx, t, vin, "getWarrantyInfo", warrantyFields, false, &out)
	return out.Items, e
}
func (c *Client) SxmSubscription(ctx context.Context, t GMToken, vin string) (SxmInfo, error) {
	var out struct {
		Info SxmInfo `json:"sxmSubscriptionInfo"`
	}
	_, e := c.garageDetails(ctx, t, vin, "getSxmSubscriptionInfo", sxmFields, false, &out)
	return out.Info, e
}
