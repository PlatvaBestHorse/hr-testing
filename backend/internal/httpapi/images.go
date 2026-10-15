package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const maxImageBytes = 5 * 1024 * 1024
const imageQuota = 100 * 1024 * 1024

func optionalUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

type storedImage struct {
	ID     string `json:"id"`
	MIME   string `json:"mime_type"`
	Size   int    `json:"byte_size"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Raster decoding and re-encoding strips metadata, filenames and trailing
// payloads. SVG/GIF and unknown formats are deliberately not accepted.
func normalizeImage(raw []byte, declared string) ([]byte, storedImage, error) {
	var meta storedImage
	if len(raw) == 0 || len(raw) > maxImageBytes {
		return nil, meta, fmt.Errorf("Изображение должно занимать от 1 байта до 5 МиБ")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "jpeg") {
		return nil, meta, fmt.Errorf("Нужен корректный файл PNG или JPEG")
	}
	meta.MIME = "image/" + format
	if declared != meta.MIME {
		return nil, meta, fmt.Errorf("Тип файла не соответствует содержимому PNG/JPEG")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > 8000000 {
		return nil, meta, fmt.Errorf("Максимум 4096 пикселей по стороне и 8 миллионов пикселей")
	}
	decoded, actual, err := image.Decode(bytes.NewReader(raw))
	if err != nil || actual != format {
		return nil, meta, fmt.Errorf("Изображение повреждено или не поддерживается")
	}
	var output bytes.Buffer
	limited := &imageWriter{Buffer: &output}
	if format == "png" {
		err = png.Encode(limited, decoded)
	} else {
		err = jpeg.Encode(limited, decoded, &jpeg.Options{Quality: 90})
	}
	if err != nil || output.Len() > maxImageBytes {
		return nil, meta, fmt.Errorf("Изображение после обработки превышает 5 МиБ")
	}
	meta.Width = cfg.Width
	meta.Height = cfg.Height
	meta.Size = output.Len()
	return output.Bytes(), meta, nil
}

type imageWriter struct{ *bytes.Buffer }

func (w *imageWriter) Write(p []byte) (int, error) {
	if w.Len()+len(p) > maxImageBytes {
		return 0, fmt.Errorf("normalized image is too large")
	}
	return w.Buffer.Write(p)
}

func (a *App) uploadImage(w http.ResponseWriter, r *http.Request, p principal) {
	select {
	case a.imageSlots <- struct{}{}:
		defer func() { <-a.imageSlots }()
	default:
		problem(w, 503, "image_busy", "Обработка изображений занята. Попробуйте ещё раз")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImageBytes+64*1024)
	reader, err := r.MultipartReader()
	if err != nil {
		problem(w, 415, "unsupported_media_type", "Нужен multipart/form-data с одним файлом file")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		problem(w, 400, "invalid_upload", "Выберите один файл изображения")
		return
	}
	declared, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil || (declared != "image/png" && declared != "image/jpeg") {
		problem(w, 415, "unsupported_image", "Поддерживаются только PNG и JPEG")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(part, maxImageBytes+1))
	part.Close()
	if err != nil || len(raw) > maxImageBytes {
		problem(w, 413, "image_too_large", "Размер изображения превышает 5 МиБ")
		return
	}
	if _, err = reader.NextPart(); err != io.EOF {
		problem(w, 400, "invalid_upload", "Передайте ровно один файл без дополнительных полей")
		return
	}
	body, meta, err := normalizeImage(raw, declared)
	if err != nil {
		problem(w, 422, "invalid_image", err.Error())
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	// Serialize quota accounting without blocking FK key-share locks.
	if _, err = tx.Exec(r.Context(), "SELECT id FROM organizations WHERE id=$1 FOR NO KEY UPDATE", p.OrganizationID); err != nil {
		a.failure(w, err)
		return
	}
	var used int64
	var count int
	if err = tx.QueryRow(r.Context(), "SELECT coalesce(sum(byte_size),0),count(*) FROM attachments WHERE organization_id=$1", p.OrganizationID).Scan(&used, &count); err != nil {
		a.failure(w, err)
		return
	}
	if used+int64(meta.Size) > imageQuota || count >= 1000 {
		problem(w, 422, "image_quota", "Лимит организации — 100 МиБ и 1000 изображений. Удалите неиспользуемые загрузки")
		return
	}
	hash := sha256.Sum256(body)
	err = tx.QueryRow(r.Context(), `INSERT INTO attachments(organization_id,storage_key,mime_type,byte_size,sha256,uploaded_by,data,width,height)
 VALUES($1,'pg:'||gen_random_uuid(),$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.OrganizationID, meta.MIME, meta.Size, hex.EncodeToString(hash[:]), p.UserID, body, meta.Width, meta.Height).Scan(&meta.ID)
	if err == nil {
		err = a.audit(r.Context(), tx, p, "image.upload", "attachment", meta.ID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 201, meta)
}

func (a *App) unusedImages(w http.ResponseWriter, r *http.Request, p principal) {
	page, ok := listPage(w, r)
	if !ok {
		return
	}
	where := ` FROM attachments a WHERE a.organization_id=$1 AND a.data IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM question_versions v WHERE v.organization_id=a.organization_id AND v.image_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM question_options o WHERE o.organization_id=a.organization_id AND o.image_id=a.id)`
	var total int
	if err := a.Pool.QueryRow(r.Context(), "SELECT count(*)"+where, p.OrganizationID).Scan(&total); err != nil {
		a.failure(w, err)
		return
	}
	rows, err := a.Pool.Query(r.Context(), "SELECT a.id,a.mime_type,a.byte_size,a.width,a.height"+where+" ORDER BY a.created_at DESC,a.id LIMIT 20 OFFSET $2", p.OrganizationID, (page-1)*20)
	if err != nil {
		a.failure(w, err)
		return
	}
	defer rows.Close()
	items := []storedImage{}
	for rows.Next() {
		var item storedImage
		if err = rows.Scan(&item.ID, &item.MIME, &item.Size, &item.Width, &item.Height); err != nil {
			a.failure(w, err)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 20})
}

func imageBytes(w http.ResponseWriter, meta storedImage, body []byte) {
	w.Header().Set("Content-Type", meta.MIME)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	ext := "png"
	if meta.MIME == "image/jpeg" {
		ext = "jpg"
	}
	w.Header().Set("Content-Disposition", `inline; filename="image.`+ext+`"`)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.WriteHeader(200)
	w.Write(body)
}

func readImage(ctx context.Context, tx pgx.Tx, org, id string) (storedImage, []byte, error) {
	var meta storedImage
	var body []byte
	err := tx.QueryRow(ctx, `SELECT id,mime_type,byte_size,width,height,data FROM attachments WHERE organization_id=$1 AND id=$2 AND data IS NOT NULL`, org, id).Scan(&meta.ID, &meta.MIME, &meta.Size, &meta.Width, &meta.Height, &body)
	return meta, body, err
}

func (a *App) hrImage(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("image")
	if !uuid(id) {
		problem(w, 404, "not_found", "Изображение не найдено")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	meta, body, err := readImage(r.Context(), tx, p.OrganizationID, id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	imageBytes(w, meta, body)
}

// A shared immutable reference is sufficient: attempt -> immutable test item ->
// immutable question version/options. Neither current question drafts nor the
// full test's bank are exposed to the candidate.
const attemptImageLink = `SELECT 1 FROM attempt_items ai
 JOIN test_items ti ON ti.organization_id=ai.organization_id AND ti.id=ai.test_item_id
 JOIN question_versions q ON q.organization_id=ti.organization_id AND q.id=ti.question_version_id
 WHERE ai.organization_id=$1 AND ai.attempt_id=$2 AND (q.image_id=$3 OR EXISTS(
 SELECT 1 FROM question_options opt WHERE opt.organization_id=q.organization_id AND opt.question_version_id=q.id AND opt.image_id=$3
 AND ai.option_order @> jsonb_build_array(opt.id::text)))`

func (a *App) candidateImage(w http.ResponseWriter, r *http.Request) {
	id, img := r.PathValue("id"), r.PathValue("image")
	if !uuid(id) || !uuid(img) {
		problem(w, 404, "not_found", "Изображение недоступно")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	i, err := lockInvite(r.Context(), tx, r.PathValue("token"))
	if err != nil {
		a.failure(w, err)
		return
	}
	var allowed bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM attempts WHERE organization_id=$1 AND id=$2 AND invitation_id=$4
 AND status='in_progress' AND deadline_at>clock_timestamp()) AND EXISTS(`+attemptImageLink+`) AND $5::timestamptz>clock_timestamp()`, i.Org, id, img, i.ID, i.Expiry).Scan(&allowed)
	if err != nil {
		a.failure(w, err)
		return
	}
	if !allowed {
		problem(w, 404, "not_found", "Изображение недоступно")
		return
	}
	meta, body, err := readImage(r.Context(), tx, i.Org, img)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	imageBytes(w, meta, body)
}

func (a *App) resultImage(w http.ResponseWriter, r *http.Request, p principal) {
	id, img := r.PathValue("id"), r.PathValue("image")
	if !uuid(id) || !uuid(img) {
		problem(w, 404, "not_found", "Изображение недоступно")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	// Scope uses the same assignment check as reports. Read the image in this TX.
	var status string
	err = tx.QueryRow(r.Context(), "SELECT a.status"+resultsFrom+" AND a.id=$4", p.OrganizationID, p.Role, p.UserID, id).Scan(&status)
	if err != nil {
		a.failure(w, err)
		return
	}
	if status != "submitted" && status != "expired" {
		a.reportFailure(w, errReportPending)
		return
	}
	var allowed bool
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS("+attemptImageLink+")", p.OrganizationID, id, img).Scan(&allowed); err != nil {
		a.failure(w, err)
		return
	}
	if !allowed {
		problem(w, 404, "not_found", "Изображение недоступно")
		return
	}
	meta, body, err := readImage(r.Context(), tx, p.OrganizationID, img)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	imageBytes(w, meta, body)
}

func (a *App) deleteImage(w http.ResponseWriter, r *http.Request, p principal) {
	id := r.PathValue("image")
	if !uuid(id) {
		problem(w, 404, "not_found", "Изображение не найдено")
		return
	}
	tx, err := a.Pool.Begin(r.Context())
	if err != nil {
		a.failure(w, err)
		return
	}
	defer tx.Rollback(context.Background())
	var deleted string
	err = tx.QueryRow(r.Context(), "DELETE FROM attachments WHERE organization_id=$1 AND id=$2 AND data IS NOT NULL RETURNING id", p.OrganizationID, id).Scan(&deleted)
	if err == nil {
		err = a.audit(r.Context(), tx, p, "image.delete", "attachment", id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		a.failure(w, err)
		return
	}
	w.WriteHeader(204)
}
