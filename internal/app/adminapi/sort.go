package adminapi

import (
	"net/http"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/platform/listquery"
)

// parseListSort 解析 ?sort=field|-field；非法字段写 400。
func parseListSort(r *http.Request, allowed map[string]struct{}, defaultField string, defaultDesc bool) (listquery.Sort, error) {
	return listquery.ParseSort(r, allowed, defaultField, defaultDesc)
}

func writeSortError(w http.ResponseWriter, err error) {
	_ = httpx.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error": map[string]any{
			"code":    "invalid_argument",
			"message": err.Error(),
		},
	})
}
