package obs

import "log/slog"

// LogInfo emits an INFO-level structured JSON record with the supplied
// key/value pairs as top-level attributes. The kv slice must have an even
// number of elements (key, value, key, value, ...); odd trailing values are
// passed through as-is and slog will tag them as "!BADKEY".
func (o *MemObserver) LogInfo(msg string, kv ...any) {
	o.logger.Info(msg, kv...)
}

// LogWarn emits a WARN-level structured JSON record. See LogInfo for kv conventions.
func (o *MemObserver) LogWarn(msg string, kv ...any) {
	o.logger.Warn(msg, kv...)
}

// LogError emits an ERROR-level structured JSON record. err.Error() is
// recorded under the "err" key; if err is nil the field is omitted. Caller-
// supplied kv pairs follow the err field, allowing the typical pattern of
// "err_type", "op", "box_id", etc. additions.
func (o *MemObserver) LogError(msg string, err error, kv ...any) {
	if err != nil {
		// Prepend err= so it appears before user-supplied kv. slog flattens
		// attrs into the JSON object regardless of position.
		kv = append([]any{slog.String("err", err.Error())}, kv...)
	}
	o.logger.Error(msg, kv...)
}
