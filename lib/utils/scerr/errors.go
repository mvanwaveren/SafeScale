/*
 * Copyright 2018-2019, CS Systemes d'Information, http://www.c-s.fr
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package scerr

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/CS-SI/SafeScale/lib/utils/commonlog"
)

var removePart atomic.Value

// Error defines the interface of a SafeScaoe error
type Error interface {
	AddConsequence(err error) Error
	Cause() error
	CauseFormatter() string
	Consequences() []error
	Error() string
	FieldsFormatter() string
	GRPCCode() codes.Code
	Reset(err error) Error
	ToGRPCStatus() error
	WithField(key string, value interface{}) Error
}

// AddConsequence adds an error 'err' to the list of consequences
func AddConsequence(err error, cons error) error {
	if err != nil {
		conseq, ok := err.(Error)
		if ok {
			if cons != nil {
				nerr := conseq.AddConsequence(cons)
				return nerr
			}
			return conseq
		}
		logrus.Errorf("trying to add error [%s] to existing error [%s] but failed", cons, err)
	}
	return err
}

// Consequences returns the list of consequences
func Consequences(err error) []error {
	if err != nil {
		conseq, ok := err.(Error)
		if ok {
			return conseq.Consequences()
		}
	}

	return []error{}
}

// WithField ...
func WithField(err error, key string, content interface{}) error {
	if err != nil {
		enrich, ok := err.(Error)
		if ok {
			if key != "" {
				nerr := enrich.WithField(key, content)
				return nerr
			}
			return enrich
		}
	}
	return err
}

// DecorateError changes the error to something more comprehensible when
// timeout occurred
func DecorateError(err error, action string, timeout time.Duration) error {
	if IsGRPCError(err) {
		if IsGRPCTimeout(err) {
			if timeout > 0 {
				return fmt.Errorf("%s took too long (> %v) to respond", action, timeout)
			}
			return fmt.Errorf("%s took too long to respond", action)
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "desc = ") {
		pos := strings.Index(msg, "desc = ") + 7
		msg = msg[pos:]

		if strings.Index(msg, " :") == 0 {
			msg = msg[2:]
		}
		return fmt.Errorf(msg)
	}
	return err
}

// IsGRPCTimeout tells if the err is a timeout kind
func IsGRPCTimeout(err error) bool {
	return grpcstatus.Code(err) == codes.DeadlineExceeded
}

// IsGRPCError tells if the err is of GRPC kind
func IsGRPCError(err error) bool {
	_, ok := grpcstatus.FromError(err)
	return ok
}

// FromGRPCStatus translates GRPC status to error
func FromGRPCStatus(err error) Error {
	if _, ok := err.(Error); ok {
		return err.(Error)
	}

	message := grpcstatus.Convert(err).Message()
	code := grpcstatus.Code(err)
	common := &errCore{Message: message, grpcCode: code}
	switch code {
	case codes.DeadlineExceeded:
		return &ErrTimeout{errCore: common}
	case codes.Aborted:
		return &ErrAborted{errCore: common}
	case codes.FailedPrecondition:
		return &ErrInvalidParameter{errCore: common}
	case codes.AlreadyExists:
		return &ErrDuplicate{errCore: common}
	case codes.InvalidArgument:
		return &ErrInvalidRequest{errCore: common}
	case codes.NotFound:
		return &ErrNotFound{errCore: common}
	case codes.PermissionDenied:
		return &ErrForbidden{errCore: common}
	case codes.ResourceExhausted:
		return &ErrOverload{errCore: common}
	case codes.OutOfRange:
		return &ErrOverflow{errCore: common}
	case codes.Unimplemented:
		return &ErrNotImplemented{errCore: common}
	case codes.Internal:
		return &ErrRuntimePanic{errCore: common}
	case codes.DataLoss:
		return &ErrInconsistent{errCore: common}
	case codes.Unauthenticated:
		return &ErrNotAuthenticated{errCore: common}
	}
	return common
}

// ToGRPCStatus translates an error to a GRPC status
func ToGRPCStatus(err error) error {
	if casted, ok := err.(Error); ok {
		return casted.ToGRPCStatus()
	}
	return grpcstatus.Errorf(codes.Unknown, err.Error())
}

type fields map[string]interface{}

// errCore is the implementation of interface Error
type errCore struct {
	Message      string `json:"message,omitempty"`
	Causer       error  `json:"cause,omitempty"`
	Fields       fields `json:"fields,omitempty"`
	consequences []error
	grpcCode     codes.Code
}

// FieldsFormatter ...
func (e *errCore) FieldsFormatter() string {
	j, err := json.Marshal(e.Fields)

	if err != nil {
		return ""
	}

	return string(j)
}

// CauseFormatter generates a string containing information about the causing error and the derived errors while trying to clean up
func (e *errCore) CauseFormatter() string {
	msgFinal := ""

	if e.Cause() != nil {
		msgFinal += " ["
		msgFinal += "caused by {"
		msgFinal += e.Cause().Error()
		msgFinal += "}]"
	}

	lenConseq := len(e.Consequences())
	if lenConseq > 0 {
		msgFinal += "[with consequences {"
		for ind, con := range e.Consequences() {
			msgFinal += con.Error()
			if ind+1 < lenConseq {
				msgFinal += ";"
			}
		}
		msgFinal += "}]"
	}

	return msgFinal
}

// Reset imports content of error err to receiving error e
func (e *errCore) Reset(err error) Error {
	if err != nil {
		if cerr, ok := err.(*errCore); ok {
			e.Message = cerr.Message
			e.consequences = cerr.consequences
			e.Causer = cerr.Causer
		}
	}
	return e
}

// Cause returns an error's Causer
func (e *errCore) Cause() error {
	return e.Causer
}

// Consequences returns the consequences of current error (detected teardown problems)
func (e *errCore) Consequences() []error {
	return e.consequences
}

// GRPCCode returns the appropriate error code to use with gRPC
func (e *errCore) GRPCCode() codes.Code {
	return e.grpcCode
}

// ToGRPCStatus returns a grpcstatus struct from error
func (e *errCore) ToGRPCStatus() error {
	return grpcstatus.Errorf(e.GRPCCode(), e.Error())
}

// Wrap creates a new error with a message 'message' and a Causer error 'Causer'
func Wrap(cause error, message string) Error {
	newErr := &errCore{Message: message, Causer: cause, consequences: []error{}}
	if casted, ok := cause.(*errCore); ok {
		newErr.grpcCode = casted.GRPCCode()
	} else {
		newErr.grpcCode = codes.Unknown
	}
	return newErr
}

// NewError creates a new error with a message 'message', a Causer error 'Causer' and a list of teardown problems 'consequences'
func NewError(message string, cause error, consequences []error) Error {
	if consequences == nil {
		consequences = []error{}
	}
	return &errCore{
		Message:      message,
		Causer:       cause,
		consequences: consequences,
		Fields:       make(fields),
	}
}

// AddConsequence adds an error 'err' to the list of consequences
func (e *errCore) AddConsequence(err error) Error {
	if err != nil {
		if e.consequences == nil {
			e.consequences = []error{}
		}
		e.consequences = append(e.consequences, err)
	}
	return e
}

// WithField ...
func (e *errCore) WithField(key string, value interface{}) Error {
	if e.Fields != nil {
		e.Fields[key] = value
	}

	return e
}

// Error returns a human-friendly error explanation
func (e *errCore) Error() string {
	msgFinal := e.Message

	msgFinal += e.CauseFormatter()

	if len(e.Fields) > 0 {
		msgFinal += "\nWith fields: "
		msgFinal += e.FieldsFormatter()
	}

	return msgFinal
}

// Cause returns the Causer of an error if it implements the causer interface
func Cause(err error) (resp error) {
	resp = err

	for err != nil {
		cause, ok := err.(Error)
		if !ok {
			break
		}
		err = cause.Cause()
		if err != nil {
			resp = err
		}
	}

	return resp
}

// ErrTimeout defines a Timeout error
type ErrTimeout struct {
	*errCore
	dur time.Duration
}

// TimeoutError returns an ErrTimeout instance
func TimeoutError(msg string, timeout time.Duration, cause error) *ErrTimeout {
	return &ErrTimeout{
		errCore: &errCore{
			Message:      msg,
			Causer:       cause,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.DeadlineExceeded,
		},
		dur: timeout,
	}
}

// ErrNotFound resource not found error
type ErrNotFound struct {
	*errCore
}

// NotFoundError creates a ErrNotFound error
func NotFoundError(msg string) *ErrNotFound {
	return &ErrNotFound{
		errCore: &errCore{
			Message:      msg,
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.NotFound,
		},
	}
}

// ErrNotAvailable resource not available error
type ErrNotAvailable struct {
	*errCore
}

// NotAvailableError creates a NotAvailable error
func NotAvailableError(msg string) *ErrNotAvailable {
	return &ErrNotAvailable{
		errCore: &errCore{
			Message:      msg,
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.Unavailable,
		},
	}
}

// ErrDuplicate already exists error
type ErrDuplicate struct {
	*errCore
}

// DuplicateError creates a ErrDuplicate error
func DuplicateError(msg string) *ErrDuplicate {
	return &ErrDuplicate{
		errCore: &errCore{
			Message:      msg,
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.AlreadyExists,
		},
	}
}

// ErrInvalidRequest ...
type ErrInvalidRequest struct {
	*errCore
}

// InvalidRequestError creates a ErrInvalidRequest error
func InvalidRequestError(msg string) *ErrInvalidRequest {
	return &ErrInvalidRequest{
		errCore: &errCore{
			Message:      msg,
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.InvalidArgument,
		},
	}
}

// ErrNotAuthenticated when action is done without being authenticated first
type ErrNotAuthenticated struct {
	*errCore
}

// NotAuthenticatedError creates a ErrNotAuthenticated error
func NotAuthenticatedError(msg string) *ErrNotAuthenticated {
	return &ErrNotAuthenticated{
		errCore: &errCore{
			Message:      msg,
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.Unauthenticated,
		},
	}
}

// ErrForbidden when action is not allowed.
type ErrForbidden struct {
	*errCore
}

// ForbiddenError creates a ErrForbidden error
func ForbiddenError(msg string) *ErrForbidden {
	return &ErrForbidden{
		errCore: &errCore{
			Message:      msg,
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.PermissionDenied,
		},
	}
}

// ErrAborted ...
type ErrAborted struct {
	*errCore
}

// AbortedError creates a ErrAborted error
func AbortedError(msg string, err error) *ErrAborted {
	if msg == "" {
		msg = "aborted"
	}
	return &ErrAborted{
		errCore: &errCore{
			Message:      msg,
			Causer:       err,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.Aborted,
		},
	}
}

// ErrOverflow is used when a limit is reached
type ErrOverflow struct {
	*errCore
}

// OverflowError creates a ErrOverflow error
func OverflowError(msg string) *ErrOverflow {
	return &ErrOverflow{
		errCore: &errCore{
			Message:      msg,
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.OutOfRange,
		},
	}
}

// ErrOverload when action cannot be honored because provider is overloaded (ie too many requests occured in a given time).
type ErrOverload struct {
	*errCore
}

// OverloadError creates a ErrOverload error
func OverloadError(msg string) *ErrOverload {
	return &ErrOverload{
		errCore: &errCore{
			Message:      msg,
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.ResourceExhausted,
		},
	}
}

// ErrNotImplemented ...
type ErrNotImplemented struct {
	*errCore
}

// NotImplementedError creates a ErrNotImplemented error
func NotImplementedError(what string) *ErrNotImplemented {
	return &ErrNotImplemented{
		errCore: &errCore{
			Message:      decorateWithCallTrace("not implemented yet:", what, ""),
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.Unimplemented,
		},
	}
}

// NotImplementedErrorWithReason creates a ErrNotImplemented error
func NotImplementedErrorWithReason(what string, why string) *ErrNotImplemented {
	return &ErrNotImplemented{
		errCore: &errCore{
			Message:      decorateWithCallTrace("not implemented yet:", what, why),
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
		},
	}
}

// ErrList ...
type ErrList struct {
	*errCore
	errors []error
}

// ErrListError creates a ErrList
func ErrListError(errors []error) error {
	if len(errors) == 0 {
		return nil
	}

	return &ErrList{
		errCore: &errCore{},
		errors:  errors,
	}
}

func (e *ErrList) Error() string {
	return spew.Sdump(e.errors)
}

// ErrRuntimePanic ...
type ErrRuntimePanic struct {
	*errCore
}

// RuntimePanicError creates a ErrRuntimePanic error
func RuntimePanicError(msg string) *ErrRuntimePanic {
	return &ErrRuntimePanic{
		errCore: &errCore{
			Message:      msg,
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.Internal,
		},
	}
}

// ErrInvalidInstance has to be used when a method is called from an instance equal to nil
type ErrInvalidInstance struct {
	*errCore
}

// InvalidInstanceError creates a ErrInvalidInstance error
func InvalidInstanceError() *ErrInvalidInstance {
	return &ErrInvalidInstance{
		errCore: &errCore{
			Message:      decorateWithCallTrace("invalid instance:", "", "calling method from a nil pointer"),
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.FailedPrecondition,
		},
	}
}

// ErrInvalidParameter ...
type ErrInvalidParameter struct {
	*errCore
}

// InvalidParameterError creates a ErrInvalidParameter error
func InvalidParameterError(what, why string) *ErrInvalidParameter {
	return &ErrInvalidParameter{
		errCore: &errCore{
			Message:      decorateWithCallTrace("invalid parameter:", what, why),
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.FailedPrecondition,
		},
	}
}

// decorateWithCallTrace adds call trace to the message "prefix what: why"
func decorateWithCallTrace(prefix, what, why string) string {
	const missingPrefixMessage = "uncategorized error occurred:"

	msg := prefix
	if prefix == "" {
		prefix = missingPrefixMessage
	}

	if what != "" {
		msg += " '" + what + "'"
	}

	if pc, file, line, ok := runtime.Caller(2); ok {
		if f := runtime.FuncForPC(pc); f != nil {
			filename := strings.Replace(file, getPartToRemove(), "", 1)
			if what == "" {
				msg += fmt.Sprintf(" %s", filepath.Base(f.Name()))
			} else {
				msg += fmt.Sprintf(" in %s", filepath.Base(f.Name()))
			}
			if why != "" {
				msg += ": " + why
			}
			msg += fmt.Sprintf(" [%s:%d]", filename, line)
		}
	} else {
		if why != "" {
			msg += ": " + why
		}
	}
	msg += "\n" + string(debug.Stack())
	return msg
}

// ErrInvalidInstanceContent has to be used when a property of an instance contains invalid property
type ErrInvalidInstanceContent struct {
	*errCore
}

// InvalidInstanceContentError returns an instance of ErrInvalidInstanceContent.
func InvalidInstanceContentError(what, why string) *ErrInvalidInstanceContent {
	return &ErrInvalidInstanceContent{
		errCore: &errCore{
			Message:      decorateWithCallTrace("invalid instance content:", what, why),
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.FailedPrecondition,
		},
	}
}

// ErrInconsistent is used when data used is inconsistent
type ErrInconsistent struct {
	*errCore
}

// InconsistentError creates a ErrInconsistent error
func InconsistentError(msg string) *ErrInconsistent {
	return &ErrInconsistent{
		errCore: &errCore{
			Message:      decorateWithCallTrace(msg, "", ""),
			Causer:       nil,
			consequences: []error{},
			Fields:       make(fields),
			grpcCode:     codes.DataLoss,
		},
	}
}

// getPartToRemove returns the part of the file path to remove before display.
func getPartToRemove() string {
	if anon := removePart.Load(); anon != nil {
		return anon.(string)
	}
	return "github.com/CS-SI/SafeScale/"
}

// ----------- log helpers ---------------

const (
	errorOccurred       = "ERROR OCCURRED"
	outputErrorTemplate = "%s " + errorOccurred + ": %+v"
)

// OnExitLogErrorWithLevel returns a function that will log error with the log level wanted
// Intended to be used with defer for example.
func OnExitLogErrorWithLevel(in string, err *error, level logrus.Level) func() {
	// If there is nothing to log, or if err is a grpc one, returns an empty function
	if in == "" {
		return func() {}
	}

	logLevelFn, ok := commonlog.LogLevelFnMap[level]
	if !ok {
		logLevelFn = logrus.Error
	}

	if IsGRPCError(*err) {
		return func() {
			if err != nil && *err != nil {
				logLevelFn(fmt.Sprintf(outputErrorTemplate, in, grpcstatus.Convert(*err).Message()))
			}
		}
	}

	// if 'in' is empty, recover function name from caller
	if len(in) == 0 {
		if pc, _, _, ok := runtime.Caller(1); ok {
			if f := runtime.FuncForPC(pc); f != nil {
				in = filepath.Base(f.Name())
			}
		}
	}

	return func() {
		if err != nil && *err != nil {
			logLevelFn(fmt.Sprintf(outputErrorTemplate, in, *err))
		}
	}
}

// OnExitLogError returns a function that will log error with level logrus.ErrorLevel.
// Intended to be used with defer for example
func OnExitLogError(in string, err *error) func() {
	return OnExitLogErrorWithLevel(in, err, logrus.ErrorLevel)
}

// OnExitTraceError returns a function that will log error with level logrus.TraceLevel.
// Intended to be used with defer for example.
func OnExitTraceError(in string, err *error) func() {
	return OnExitLogErrorWithLevel(in, err, logrus.TraceLevel)
}

// OnPanic returns a function intended to capture panic error and fill the error pointer with a ErrRuntimePanic.
func OnPanic(err *error) func() {
	return func() {
		if x := recover(); x != nil {
			*err = RuntimePanicError(fmt.Sprintf("runtime panic occurred: %+v", x))
		}
	}
}

func init() {
	var rootPath string
	if pc, _, _, ok := runtime.Caller(0); ok {
		if f := runtime.FuncForPC(pc); f != nil {
			rootPath = strings.Split(f.Name(), "lib/utils/")[0]
		}
	}
	removePart.Store(rootPath)
}
