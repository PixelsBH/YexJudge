package judge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func buildCppFunctionHarness(job Job) (string, error) {
	var b strings.Builder

	b.WriteString("#include <bits/stdc++.h>\n")
	b.WriteString("using namespace std;\n\n")
	b.WriteString(job.SourceCode)
	b.WriteString("\n\n")
	b.WriteString(cppSerializerSource())
	b.WriteString("\nint main() {\n")
	b.WriteString("    int __case_id;\n")
	b.WriteString("    if (!(cin >> __case_id)) return 1;\n")
	b.WriteString("    Solution __solution;\n")
	b.WriteString("    switch (__case_id) {\n")

	for _, tc := range job.TestCases {
		caseSource, err := buildCppFunctionCase(job, tc)
		if err != nil {
			return "", err
		}
		b.WriteString(caseSource)
	}

	b.WriteString("    default:\n")
	b.WriteString("        return 1;\n")
	b.WriteString("    }\n")
	b.WriteString("    return 0;\n")
	b.WriteString("}\n")

	return b.String(), nil
}

func buildCppFunctionCase(job Job, tc TestCase) (string, error) {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("    case %d: {\n", tc.ID))

	args := make([]string, 0, len(job.Function.Params))
	for i, param := range job.Function.Params {
		storageType := cppStorageType(param.Type)
		value, err := cppValueLiteral(storageType, tc.Args[i])
		if err != nil {
			return "", fmt.Errorf("test case %d param %s: %w", tc.ID, param.Name, err)
		}

		b.WriteString(fmt.Sprintf("        %s %s = %s;\n", storageType, param.Name, value))
		args = append(args, param.Name)
	}

	b.WriteString(fmt.Sprintf(
		"        auto __result = __solution.%s(%s);\n",
		job.Function.Name,
		strings.Join(args, ", "),
	))
	b.WriteString("        cout << __serialize(__result);\n")
	b.WriteString("        break;\n")
	b.WriteString("    }\n")

	return b.String(), nil
}

func cppStorageType(cppType string) string {
	cppType = strings.TrimSpace(cppType)
	cppType = strings.TrimPrefix(cppType, "const ")
	cppType = strings.TrimSpace(strings.TrimSuffix(cppType, "&"))
	return cppType
}

func cppValueLiteral(cppType string, raw json.RawMessage) (string, error) {
	switch cppType {
	case "int", "long long", "double":
		var value json.Number
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		return value.String(), nil
	case "bool":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		if value {
			return "true", nil
		}
		return "false", nil
	case "string":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		return strconv.Quote(value), nil
	}

	if elementType, ok := strings.CutPrefix(cppType, "vector<"); ok && strings.HasSuffix(elementType, ">") {
		elementType = strings.TrimSuffix(elementType, ">")
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return "", err
		}

		literals := make([]string, 0, len(values))
		for _, value := range values {
			literal, err := cppValueLiteral(elementType, value)
			if err != nil {
				return "", err
			}
			literals = append(literals, literal)
		}

		return fmt.Sprintf("%s{%s}", cppType, strings.Join(literals, ", ")), nil
	}

	return "", fmt.Errorf("unsupported cpp type %q", cppType)
}

func normalizeExpectedJSON(raw json.RawMessage) (string, error) {
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, raw); err != nil {
		return "", err
	}
	return compacted.String(), nil
}

func cppSerializerSource() string {
	return `
static string __escape_json_string(const string& value) {
    string out = "\"";
    for (char ch : value) {
        if (ch == '\\') out += "\\\\";
        else if (ch == '"') out += "\\\"";
        else if (ch == '\n') out += "\\n";
        else if (ch == '\r') out += "\\r";
        else if (ch == '\t') out += "\\t";
        else out += ch;
    }
    out += "\"";
    return out;
}

static string __serialize(int value) {
    return to_string(value);
}

static string __serialize(long long value) {
    return to_string(value);
}

static string __serialize(double value) {
    ostringstream out;
    out << value;
    return out.str();
}

static string __serialize(bool value) {
    return value ? "true" : "false";
}

static string __serialize(const string& value) {
    return __escape_json_string(value);
}

static string __serialize(const vector<bool>& values) {
    string out = "[";
    for (size_t i = 0; i < values.size(); i++) {
        if (i > 0) out += ",";
        out += (values[i] ? "true" : "false");
    }
    out += "]";
    return out;
}

template <typename T>
static string __serialize(const vector<T>& values) {
    string out = "[";
    for (size_t i = 0; i < values.size(); i++) {
        if (i > 0) out += ",";
        out += __serialize(values[i]);
    }
    out += "]";
    return out;
}
`
}
