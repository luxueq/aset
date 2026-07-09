package provider

// ParamPolicy defines how to handle unsupported parameters.
type ParamPolicy int

const (
	// ParamDrop silently removes unsupported parameters (default).
	ParamDrop ParamPolicy = iota
	// ParamStrict returns an error for unsupported parameters.
	ParamStrict
)

// SupportedParams defines the parameters each provider supports.
// This is used by ConvertRequest to drop unsupported params.
var SupportedParams = map[string]map[string]bool{
	"openai": {
		"model": true, "messages": true, "stream": true,
		"temperature": true, "max_tokens": true, "top_p": true,
		"stop": true, "seed": true, "presence_penalty": true,
		"frequency_penalty": true, "logit_bias": true, "n": true,
		"response_format": true, "tools": true, "tool_choice": true,
	},
	"anthropic": {
		"model": true, "messages": true, "stream": true,
		"temperature": true, "max_tokens": true, "top_p": true,
		"stop": true, "system": true, "top_k": true,
		"metadata": true, "tools": true, "tool_choice": true,
	},
	"dashscope": {
		"model": true, "messages": true, "stream": true,
		"temperature": true, "max_tokens": true, "top_p": true,
		"stop": true, "seed": true, "tools": true, "tool_choice": true,
	},
}

// FilterUnsupportedParams removes parameters not supported by the target provider.
// Returns a new map with only supported parameters.
func FilterUnsupportedParams(params map[string]interface{}, providerName string, policy ParamPolicy) (map[string]interface{}, []string, error) {
	supported, ok := SupportedParams[providerName]
	if !ok {
		// Unknown provider, pass all params through
		return params, nil, nil
	}

	filtered := make(map[string]interface{})
	var dropped []string

	for key, value := range params {
		if supported[key] {
			filtered[key] = value
		} else {
			dropped = append(dropped, key)
			if policy == ParamStrict {
				return nil, dropped, &UnsupportedParamError{Param: key, Provider: providerName}
			}
		}
	}

	return filtered, dropped, nil
}

// UnsupportedParamError is returned in strict mode when an unsupported param is found.
type UnsupportedParamError struct {
	Param    string
	Provider string
}

func (e *UnsupportedParamError) Error() string {
	return "parameter '" + e.Param + "' is not supported by provider '" + e.Provider + "'"
}
