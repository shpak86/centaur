package tunnel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxModelBodyBytes ограничивает тело, которое читаем ради имени модели:
// /api/chat с картинками (images) бывает в десятки мегабайт.
const maxModelBodyBytes = 1 << 20

// modelPaths — ручки, в теле запроса которых клиент передаёт имя модели.
var modelPaths = map[string]struct{}{
	"/api/generate":        {},
	"/api/chat":            {},
	"/api/embed":           {},
	"/api/embeddings":      {},
	"/v1/chat/completions": {},
	"/v1/completions":      {},
	"/v1/embeddings":       {},
}

// modelBody — минимальная часть тела запроса, нужная для маршрутизации.
type modelBody struct {
	Model string `json:"model"`
}

// getModel читает тело запроса на модельных ручках и возвращает имя модели.
// Тело при этом не теряется: оно целиком возвращается в req.Body, потому что
// RoundTrip следом пишет его в туннель через req.Write.
//
// Пустая строка без ошибки означает, что модели в запросе нет: ручка не
// модельная, тело пустое/сжатое/больше лимита или в JSON нет поля model.
// Ошибка возвращается, если тело не удалось прочитать или разобрать как JSON.
func getModel(req *http.Request) (string, error) {
	if req.Method != http.MethodPost || req.Body == nil {
		return "", nil
	}
	if _, ok := modelPaths[req.URL.Path]; !ok {
		return "", nil
	}
	// Сжатое тело не разобрать без распаковки, а пересылать его надо как есть.
	if req.Header.Get("Content-Encoding") != "" {
		return "", nil
	}

	body := req.Body
	// Читаем на байт больше лимита, чтобы отличить «ровно лимит» от «больше».
	buf, err := io.ReadAll(io.LimitReader(body, maxModelBodyBytes+1))
	if err != nil {
		return "", fmt.Errorf("read request body: %w", err)
	}
	if len(buf) == 0 {
		return "", nil
	}

	if len(buf) > maxModelBodyBytes {
		// Тело слишком большое: модель не достаём, но прочитанный префикс
		// возвращаем в поток, чтобы запрос ушёл в туннель целиком.
		req.Body = readCloser{
			Reader: io.MultiReader(bytes.NewReader(buf), body),
			Closer: body,
		}
		return "", nil
	}

	// Восстанавливаем тело: его пишет req.Write в RoundTrip.
	// ContentLength обязателен: Request.Write берёт длину из него, а при нуле
	// с непустым телом запрос ушёл бы chunked-ом. GetBody держим валидным,
	// чтобы запрос оставался корректным для повторной отправки.
	req.ContentLength = int64(len(buf))
	req.Body = io.NopCloser(bytes.NewReader(buf))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf)), nil
	}

	var parsed modelBody
	if err := json.Unmarshal(buf, &parsed); err != nil {
		return "", fmt.Errorf("%w: %w", errBadRequestBody, err)
	}
	return parsed.Model, nil
}

// readCloser соединяет прочитанный префикс с остатком тела, сохраняя Close.
type readCloser struct {
	io.Reader
	io.Closer
}
