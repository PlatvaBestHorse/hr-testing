package httpapi

import (
	"net/http"
	"strconv"
)

func listPage(w http.ResponseWriter, r *http.Request) (int, bool) {
	if value := r.URL.Query().Get("page"); value != "" {
		page, err := strconv.Atoi(value)
		if err != nil || page < 1 || page > 100000 {
			problem(w, 400, "invalid_page", "Некорректная страница")
			return 0, false
		}
		return page, true
	}
	return 1, true
}
