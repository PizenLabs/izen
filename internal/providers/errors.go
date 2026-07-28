package providers

import (
	"encoding/json"
	"strings"
)

type apiErrorBody struct {
	Error *apiErrorDetail `json:"error"`
}

type apiErrorDetail struct {
	Message string `json:"message"`
	Code    int    `json:"code,omitempty"`
	Type    string `json:"type,omitempty"`
}

type openAIErrorBody struct {
	Error *openAIErrorDetail `json:"error"`
}

type openAIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
}

type ollamaErrorBody struct {
	Error string `json:"error"`
}

func sanitizeAPIError(errStr string) string {
	if errStr == "" {
		return ""
	}
	colonIdx := strings.Index(errStr, ": ")
	if colonIdx < 0 {
		return errStr
	}
	prefix := errStr[:colonIdx]
	body := errStr[colonIdx+2:]

	if strings.HasPrefix(body, "{") {
		cleaned := tryParseJSONError(body)
		if cleaned != "" {
			return prefix + ": " + cleaned
		}
	}
	return prefix + ": " + body
}

func tryParseJSONError(body string) string {
	var apiErr apiErrorBody
	if err := json.Unmarshal([]byte(body), &apiErr); err == nil && apiErr.Error != nil {
		msg := apiErr.Error.Message
		if msg == "" {
			msg = apiErr.Error.Type
		}
		if apiErr.Error.Code > 0 {
			if msg != "" {
				return msg + " (" + itoa(apiErr.Error.Code) + ")"
			}
			return "error " + itoa(apiErr.Error.Code)
		}
		if msg != "" {
			return msg
		}
	}

	var oaiErr openAIErrorBody
	if err := json.Unmarshal([]byte(body), &oaiErr); err == nil && oaiErr.Error != nil {
		msg := oaiErr.Error.Message
		if oaiErr.Error.Code != "" {
			if msg != "" {
				return msg + " (" + oaiErr.Error.Code + ")"
			}
			return "error " + oaiErr.Error.Code
		}
		if msg != "" {
			return msg
		}
	}

	var ollamaErr ollamaErrorBody
	if err := json.Unmarshal([]byte(body), &ollamaErr); err == nil && ollamaErr.Error != "" {
		return ollamaErr.Error
	}

	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func SanitizeAPIError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeAPIError(err.Error())
}
