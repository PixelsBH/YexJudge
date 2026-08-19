package cpp

const serializerSource = `
static string __escape_json_string(const string& value) {
    string out = "\"";
    for (unsigned char ch : value) {
        if (ch == '\\') out += "\\\\";
        else if (ch == '\"') out += "\\\"";
        else if (ch == '\n') out += "\\n";
        else if (ch == '\r') out += "\\r";
        else if (ch == '\t') out += "\\t";
        else if (ch == '\b') out += "\\b";
        else if (ch == '\f') out += "\\f";
        else if (ch < 0x20) {
            const char* hex = "0123456789abcdef";
            out += "\\u00";
            out += hex[(ch >> 4) & 0x0f];
            out += hex[ch & 0x0f];
        } else out += static_cast<char>(ch);
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
    char buffer[64];
    auto result = to_chars(buffer, buffer + sizeof(buffer), value);
    if (result.ec == errc()) return string(buffer, result.ptr);
    ostringstream fallback;
    fallback << setprecision(numeric_limits<double>::max_digits10) << value;
    return fallback.str();
}

static string __serialize(bool value) {
    return value ? "true" : "false";
}

static string __serialize(const string& value) {
    return __escape_json_string(value);
}

template <typename T>
static string __serialize(const optional<T>& value) {
    if (!value.has_value()) return "null";
    return __serialize(*value);
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

template <typename T>
static string __serialize_prefix(const vector<T>& values, long long length) {
    if (length < 0) return "[]";
    size_t count = min(values.size(), static_cast<size_t>(length));
    string out = "[";
    for (size_t i = 0; i < count; i++) {
        if (i > 0) out += ",";
        out += __serialize(values[i]);
    }
    out += "]";
    return out;
}
`
