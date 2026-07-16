package responses

import gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"

func responsesIntPtr(v *gatewayapi.ResponsesInt) *int {
	if v == nil {
		return nil
	}
	n := v.Int()
	return &n
}
