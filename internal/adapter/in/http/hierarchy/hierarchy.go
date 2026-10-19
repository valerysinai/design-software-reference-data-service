// Package hierarchy adapts the institutional hierarchy query use case to HTTP.
package hierarchy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/security"
	application "github.com/code-sena/design-software-reference-data-service/internal/application/hierarchy"
)

const requiredFeature = "REF_HIERARCHY_VIEW"
const catalogFeature = "REF_CATALOG_VIEW"
const parameterFeature = "REF_PARAMETER_VIEW"
const manageFeature = "REF_HIERARCHY_MANAGE"
const parameterManageFeature = "REF_PARAMETER_MANAGE"

var errInvalidParameterValue = errors.New("value no es compatible con value_type")

type resource struct {
	filters    map[string]filter
	sortFields map[string]string
}
type filter struct {
	kind string
	max  int
}

var resources = map[string]resource{
	"catalogs":            {map[string]filter{"code": {"string", 50}, "is_active": {"bool", 0}}, map[string]string{"id": "id", "code": "code", "name": "name", "created_at": "created_at"}},
	"parameters":          {map[string]filter{"key": {"string", 100}, "value_type": {"value_type", 0}}, map[string]string{"id": "id", "key": "key", "value": "value", "value_type": "value_type", "created_at": "created_at"}},
	"macroregions":        {map[string]filter{"code": {"string", 10}, "is_active": {"bool", 0}}, map[string]string{"id": "id", "name": "name", "code": "code"}},
	"microregions":        {map[string]filter{"macroregion_id": {"uuid", 0}, "code": {"string", 10}, "is_active": {"bool", 0}}, map[string]string{"id": "id", "name": "name", "code": "code", "macroregion_id": "macroregion_id"}},
	"departments":         {map[string]filter{"microregion_id": {"uuid", 0}, "dane_code": {"string", 5}, "is_active": {"bool", 0}}, map[string]string{"id": "id", "name": "name", "dane_code": "dane_code", "microregion_id": "microregion_id"}},
	"municipalities":      {map[string]filter{"department_id": {"uuid", 0}, "dane_code": {"string", 8}, "is_active": {"bool", 0}}, map[string]string{"id": "id", "name": "name", "dane_code": "dane_code", "department_id": "department_id"}},
	"training-centers":    {map[string]filter{"municipality_id": {"uuid", 0}, "center_code": {"string", 10}, "is_active": {"bool", 0}}, map[string]string{"id": "id", "name": "name", "center_code": "center_code", "municipality_id": "municipality_id", "created_at": "created_at", "updated_at": "updated_at"}},
	"institutional-units": {map[string]filter{"training_center_id": {"uuid", 0}, "unit_type": {"unit_type", 0}, "is_active": {"bool", 0}}, map[string]string{"id": "id", "name": "name", "unit_type": "unit_type", "training_center_id": "training_center_id", "created_at": "created_at"}},
}

var catalogDetailsResource = resource{
	filters:    map[string]filter{"code": {"string", 50}, "is_active": {"bool", 0}},
	sortFields: map[string]string{"id": "id", "code": "code", "display_order": "display_order", "created_at": "created_at"},
}

// Handler does not log result objects: training_center.phone is PII.
type Handler struct {
	queries  *application.QueryService
	commands *application.CommandService
}

func NewHandler(queries *application.QueryService, commands ...*application.CommandService) *Handler {
	h := &Handler{queries: queries}
	if len(commands) > 0 {
		h.commands = commands[0]
	}
	return h
}
func (h *Handler) Routes() []security.ProtectedRoute {
	routes := make([]security.ProtectedRoute, 0, len(resources)*2+14)
	for name := range resources {
		name := name
		feature := requiredFeature
		if name == "catalogs" {
			feature = catalogFeature
		}
		if name == "parameters" {
			feature = parameterFeature
		}
		routes = append(routes, security.ProtectedRoute{Method: http.MethodGet, Pattern: "/" + name, Feature: feature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.list(name, w, r) })}, security.ProtectedRoute{Method: http.MethodGet, Pattern: "/" + name + "/{id}", Feature: feature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.get(name, w, r) })})
	}
	routes = append(routes,
		security.ProtectedRoute{Method: http.MethodGet, Pattern: "/catalogs/{catalog_id}/details", Feature: catalogFeature, Handler: http.HandlerFunc(h.listCatalogDetails)},
		security.ProtectedRoute{Method: http.MethodGet, Pattern: "/reports/active-catalog", Feature: catalogFeature, Handler: http.HandlerFunc(h.reportActiveCatalog)},
		security.ProtectedRoute{Method: http.MethodPost, Pattern: "/parameters", Feature: parameterManageFeature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.create("parameters", w, r) })},
		security.ProtectedRoute{Method: http.MethodPut, Pattern: "/parameters/{id}", Feature: parameterManageFeature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.update("parameters", w, r) })},
	)
	for _, name := range []string{"macroregions", "microregions", "departments", "municipalities"} {
		name := name
		routes = append(routes,
			security.ProtectedRoute{Method: http.MethodPost, Pattern: "/" + name, Feature: manageFeature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.create(name, w, r) })},
			security.ProtectedRoute{Method: http.MethodPut, Pattern: "/" + name + "/{id}", Feature: manageFeature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.update(name, w, r) })},
			security.ProtectedRoute{Method: http.MethodDelete, Pattern: "/" + name + "/{id}", Feature: manageFeature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.deactivate(name, w, r) })},
		)
	}
	for _, name := range []string{"training-centers", "institutional-units"} {
		name := name
		routes = append(routes,
			security.ProtectedRoute{Method: http.MethodPost, Pattern: "/" + name, Feature: manageFeature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.create(name, w, r) })},
			security.ProtectedRoute{Method: http.MethodPut, Pattern: "/" + name + "/{id}", Feature: manageFeature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.update(name, w, r) })},
			security.ProtectedRoute{Method: http.MethodDelete, Pattern: "/" + name + "/{id}", Feature: manageFeature, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.deactivate(name, w, r) })},
		)
	}
	routes = append(routes,
		security.ProtectedRoute{Method: http.MethodPost, Pattern: "/catalogs", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.create("catalogs", w, r) })},
		security.ProtectedRoute{Method: http.MethodPut, Pattern: "/catalogs", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.update("catalogs", w, r) })},
		security.ProtectedRoute{Method: http.MethodDelete, Pattern: "/catalogs", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.deactivate("catalogs", w, r) })},
		security.ProtectedRoute{Method: http.MethodPost, Pattern: "/catalogs/{id}", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.create("catalogs", w, r) })},
		security.ProtectedRoute{Method: http.MethodPut, Pattern: "/catalogs/{id}", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.update("catalogs", w, r) })},
		security.ProtectedRoute{Method: http.MethodDelete, Pattern: "/catalogs/{id}", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.deactivate("catalogs", w, r) })},
		security.ProtectedRoute{Method: http.MethodPost, Pattern: "/catalogs/{catalog_id}/details", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(h.createCatalogDetail)},
		security.ProtectedRoute{Method: http.MethodPut, Pattern: "/catalogs/{catalog_id}/details", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(h.updateCatalogDetail)},
		security.ProtectedRoute{Method: http.MethodDelete, Pattern: "/catalogs/{catalog_id}/details", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(h.deactivateCatalogDetail)},
		security.ProtectedRoute{Method: http.MethodPost, Pattern: "/catalogs/{catalog_id}/details/{id}", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(h.createCatalogDetail)},
		security.ProtectedRoute{Method: http.MethodPut, Pattern: "/catalogs/{catalog_id}/details/{id}", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(h.updateCatalogDetail)},
		security.ProtectedRoute{Method: http.MethodDelete, Pattern: "/catalogs/{catalog_id}/details/{id}", Feature: "REF_CATALOG_MANAGE", Handler: http.HandlerFunc(h.deactivateCatalogDetail)},
	)
	return routes
}
func (h *Handler) list(name string, w http.ResponseWriter, r *http.Request) {
	page, size, err := pagination(r)
	if err != nil {
		badRequest(w, r, err)
		return
	}
	values, err := filters(r, resources[name])
	if err != nil {
		badRequest(w, r, err)
		return
	}
	order, err := sort(r, resources[name])
	if err != nil {
		badRequest(w, r, err)
		return
	}
	result, err := h.queries.List(r.Context(), application.ListQuery{Resource: name, Page: page, PageSize: size, Sort: order, Filters: values})
	if err != nil {
		unavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "pagination": map[string]int{"page": page, "page_size": size, "total_items": result.TotalItems, "total_pages": pages(result.TotalItems, size)}})
}
func (h *Handler) get(name string, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		badRequest(w, r, errors.New("id debe ser un UUID válido"))
		return
	}
	item, err := h.queries.Get(r.Context(), name, id)
	if errors.Is(err, application.ErrNotFound) {
		security.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "El recurso solicitado no existe.")
		return
	}
	if err != nil {
		unavailable(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(item)
}

func (h *Handler) listCatalogDetails(w http.ResponseWriter, r *http.Request) {
	catalogID := r.PathValue("catalog_id")
	if !isUUID(catalogID) {
		badRequest(w, r, errors.New("catalog_id debe ser un UUID válido"))
		return
	}
	page, size, err := pagination(r)
	if err != nil {
		badRequest(w, r, err)
		return
	}
	values, err := filters(r, catalogDetailsResource)
	if err != nil {
		badRequest(w, r, err)
		return
	}
	order, err := sort(r, catalogDetailsResource)
	if err != nil {
		badRequest(w, r, err)
		return
	}
	result, err := h.queries.ListCatalogDetails(r.Context(), catalogID, application.ListQuery{Page: page, PageSize: size, Sort: order, Filters: values})
	if errors.Is(err, application.ErrNotFound) {
		security.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "El catálogo solicitado no existe.")
		return
	}
	if err != nil {
		unavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result.Data, "pagination": map[string]int{"page": page, "page_size": size, "total_items": result.TotalItems, "total_pages": pages(result.TotalItems, size)}})
}

func (h *Handler) reportActiveCatalog(w http.ResponseWriter, r *http.Request) {
	page, size, err := pagination(r)
	if err != nil {
		badRequest(w, r, err)
		return
	}
	catalogCode := r.URL.Query().Get("catalog_code")
	if len(catalogCode) > 50 {
		badRequest(w, r, errors.New("catalog_code es inválido"))
		return
	}
	entries, total, err := h.queries.ReportActiveCatalog(r.Context(), catalogCode, page, size)
	if err != nil {
		unavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": entries, "pagination": map[string]int{"page": page, "page_size": size, "total_items": total, "total_pages": pages(total, size)}})
}

type writeRequest struct {
	Name             *string `json:"name"`
	Code             *string `json:"code"`
	Label            *string `json:"label"`
	Description      *string `json:"description"`
	CenterCode       *string `json:"center_code"`
	DaneCode         *string `json:"dane_code"`
	MacroregionID    *string `json:"macroregion_id"`
	MicroregionID    *string `json:"microregion_id"`
	DepartmentID     *string `json:"department_id"`
	MunicipalityID   *string `json:"municipality_id"`
	TrainingCenterID *string `json:"training_center_id"`
	Address          *string `json:"address"`
	Phone            *string `json:"phone"`
	UnitType         *string `json:"unit_type"`
	DisplayOrder     *int    `json:"display_order"`
	IsActive         *bool   `json:"is_active"`
	Key              *string `json:"key"`
	Value            *string `json:"value"`
	ValueType        *string `json:"value_type"`
}

func (h *Handler) create(name string, w http.ResponseWriter, r *http.Request) {
	command, err := commandFromRequest(name, r, true)
	if err != nil {
		if errors.Is(err, errInvalidParameterValue) {
			unprocessableEntity(w, r, err)
			return
		}
		badRequest(w, r, err)
		return
	}
	item, err := h.commands.Create(r.Context(), name, command)
	if !writeError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (h *Handler) createCatalogDetail(w http.ResponseWriter, r *http.Request) {
	catalogID := r.PathValue("catalog_id")
	if !isUUID(catalogID) {
		badRequest(w, r, errors.New("catalog_id debe ser un UUID válido"))
		return
	}
	if h.queries == nil {
		h.create("catalog-details", w, r)
		return
	}
	payload, err := h.queries.Get(r.Context(), "catalogs", catalogID)
	if errors.Is(err, application.ErrNotFound) {
		security.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "El catálogo solicitado no existe.")
		return
	}
	if err != nil {
		unavailable(w, r)
		return
	}
	var catalog struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(payload, &catalog); err != nil || catalog.Code == "" {
		badRequest(w, r, errors.New("catalog_id inválido"))
		return
	}
	command, err := commandFromRequest("catalog-details", r, true)
	if err != nil {
		badRequest(w, r, err)
		return
	}
	command.CatalogCode = &catalog.Code
	item, err := h.commands.Create(r.Context(), "catalog-details", command)
	if !writeError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (h *Handler) update(name string, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		badRequest(w, r, errors.New("id debe ser un UUID válido"))
		return
	}
	command, err := commandFromRequest(name, r, false)
	if err != nil {
		if errors.Is(err, errInvalidParameterValue) {
			unprocessableEntity(w, r, err)
			return
		}
		badRequest(w, r, err)
		return
	}
	if name == "parameters" {
		if err := h.validateParameterUpdate(r, id, &command); err != nil {
			if errors.Is(err, application.ErrNotFound) {
				security.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "El recurso solicitado no existe.")
				return
			}
			if errors.Is(err, errInvalidParameterValue) {
				unprocessableEntity(w, r, err)
				return
			}
			unavailable(w, r)
			return
		}
	}
	item, err := h.commands.Update(r.Context(), name, id, command)
	if !writeError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (h *Handler) updateCatalogDetail(w http.ResponseWriter, r *http.Request) {
	catalogID := r.PathValue("catalog_id")
	if !isUUID(catalogID) {
		badRequest(w, r, errors.New("catalog_id debe ser un UUID válido"))
		return
	}
	if h.queries == nil {
		h.update("catalog-details", w, r)
		return
	}
	payload, err := h.queries.Get(r.Context(), "catalogs", catalogID)
	if errors.Is(err, application.ErrNotFound) {
		security.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "El catálogo solicitado no existe.")
		return
	}
	if err != nil {
		unavailable(w, r)
		return
	}
	var catalog struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(payload, &catalog); err != nil || catalog.Code == "" {
		badRequest(w, r, errors.New("catalog_id inválido"))
		return
	}
	command, err := commandFromRequest("catalog-details", r, false)
	if err != nil {
		badRequest(w, r, err)
		return
	}
	command.CatalogCode = &catalog.Code
	id := r.PathValue("id")
	if !isUUID(id) {
		badRequest(w, r, errors.New("id debe ser un UUID válido"))
		return
	}
	item, err := h.commands.Update(r.Context(), "catalog-details", id, command)
	if !writeError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (h *Handler) deactivate(name string, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		badRequest(w, r, errors.New("id debe ser un UUID válido"))
		return
	}
	if !writeError(w, r, h.commands.Deactivate(r.Context(), name, id, commandActor(r))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) deactivateCatalogDetail(w http.ResponseWriter, r *http.Request) {
	catalogID := r.PathValue("catalog_id")
	detailID := r.PathValue("id")
	if !isUUID(catalogID) {
		badRequest(w, r, errors.New("catalog_id debe ser un UUID válido"))
		return
	}
	if !isUUID(detailID) {
		badRequest(w, r, errors.New("id debe ser un UUID válido"))
		return
	}
	command := commandActor(r)
	command.ParentID = &catalogID
	if !writeError(w, r, h.commands.Deactivate(r.Context(), "catalog-details", detailID, command, catalogID)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func commandActor(r *http.Request) application.Command {
	correlationID := security.RequestCorrelationID(r)
	command := application.Command{CorrelationID: &correlationID}
	if principal, ok := security.PrincipalFromContext(r.Context()); ok {
		command.CreatedBy = &principal.ActorID
	}
	return command
}

// writeError returns true only when err is nil.
func writeError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, application.ErrNotFound) {
		security.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "El recurso solicitado no existe.")
		return false
	}
	if errors.Is(err, application.ErrConflict) {
		security.WriteError(w, r, http.StatusConflict, "CONFLICT", "Ya existe un recurso con esos datos.")
		return false
	}
	unavailable(w, r)
	return false
}
func unprocessableEntity(w http.ResponseWriter, r *http.Request, err error) {
	security.WriteError(w, r, http.StatusUnprocessableEntity, "UNPROCESSABLE_ENTITY", err.Error())
}
func commandFromRequest(name string, r *http.Request, create bool) (application.Command, error) {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var request writeRequest
	if err := decoder.Decode(&request); err != nil {
		return application.Command{}, errors.New("cuerpo de solicitud inválido")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return application.Command{}, errors.New("cuerpo de solicitud inválido")
	}
	correlationID := security.RequestCorrelationID(r)
	command := application.Command{Name: request.Name, IsActive: request.IsActive, Description: request.Description, CorrelationID: &correlationID, DisplayOrder: func() *int {
		if request.DisplayOrder == nil {
			return nil
		}
		value := *request.DisplayOrder
		return &value
	}()}
	if principal, ok := security.PrincipalFromContext(r.Context()); ok {
		command.CreatedBy = &principal.ActorID
	}
	if name == "parameters" {
		command.Key, command.Value, command.ValueType = request.Key, request.Value, request.ValueType
		if !create && request.Key != nil {
			return command, errors.New("key no se puede actualizar")
		}
		if create && (request.Key == nil || request.Value == nil || request.ValueType == nil) {
			return command, errors.New("key, value y value_type son requeridos")
		}
		if request.Key != nil && (*request.Key == "" || len(*request.Key) > 100) {
			return command, errors.New("key es inválida")
		}
		if request.ValueType != nil && !validValueType(*request.ValueType) {
			return command, errors.New("value_type es inválido")
		}
		if request.Value != nil && request.ValueType != nil && !validParameterValue(*request.Value, *request.ValueType) {
			return command, errInvalidParameterValue
		}
		if !create && command.Value == nil && command.ValueType == nil && command.Description == nil {
			return command, errors.New("cuerpo de solicitud inválido")
		}
		return command, nil
	}
	if name == "catalog-details" {
		catalogID := r.PathValue("catalog_id")
		if catalogID != "" {
			command.ParentID = &catalogID
		}
		command.Name = request.Label
		command.Code = request.Code
		command.DisplayOrder = request.DisplayOrder
		if create && (request.Code == nil || request.Label == nil || *request.Code == "" || *request.Label == "") {
			return command, errors.New("code y label son requeridos")
		}
		if request.Code != nil && (*request.Code == "" || len(*request.Code) > 50) {
			return command, errors.New("code es inválido")
		}
		if request.Label != nil && (*request.Label == "" || len(*request.Label) > 255) {
			return command, errors.New("label es inválido")
		}
		if request.DisplayOrder != nil && *request.DisplayOrder < 0 {
			return command, errors.New("display_order es inválido")
		}
		if !create && command.Name == nil && command.Code == nil && command.DisplayOrder == nil && command.IsActive == nil {
			return command, errors.New("cuerpo de solicitud inválido")
		}
		return command, nil
	}
	if name == "catalogs" {
		command.Code = request.Code
		command.Description = request.Description
		if create && (request.Name == nil || *request.Name == "" || request.Code == nil || *request.Code == "") {
			return command, errors.New("name y code son requeridos")
		}
		if request.Name != nil && (*request.Name == "" || len(*request.Name) > 100) {
			return command, errors.New("name es inválido")
		}
		if request.Code != nil && (*request.Code == "" || len(*request.Code) > 50) {
			return command, errors.New("code es inválido")
		}
		if request.Description != nil && len(*request.Description) > 1000 {
			return command, errors.New("description es inválida")
		}
		if !create && command.Name == nil && command.Code == nil && command.Description == nil && command.IsActive == nil {
			return command, errors.New("cuerpo de solicitud inválido")
		}
		return command, nil
	}
	if create && (request.Name == nil || *request.Name == "") {
		return command, errors.New("name es requerido")
	}
	maxNameLength := 100
	if name == "municipalities" {
		maxNameLength = 150
	}
	if name == "training-centers" || name == "institutional-units" {
		maxNameLength = 200
	}
	if request.Name != nil && (*request.Name == "" || len(*request.Name) > maxNameLength) {
		return command, errors.New("name es inválido")
	}
	switch name {
	case "macroregions":
		command.Code = request.Code
		if (create && (request.Code == nil || *request.Code == "")) || (request.Code != nil && (*request.Code == "" || len(*request.Code) > 10)) {
			return command, errors.New("code es inválido")
		}
	case "microregions":
		command.Code, command.ParentID = request.Code, request.MacroregionID
		if (create && (request.Code == nil || request.MacroregionID == nil)) || (request.Code != nil && (*request.Code == "" || len(*request.Code) > 10)) || (request.MacroregionID != nil && !isUUID(*request.MacroregionID)) {
			return command, errors.New("macroregion_id o code es inválido")
		}
	case "departments":
		command.DaneCode, command.ParentID = request.DaneCode, request.MicroregionID
		if (create && (request.DaneCode == nil || request.MicroregionID == nil)) || (request.DaneCode != nil && (*request.DaneCode == "" || len(*request.DaneCode) > 5)) || (request.MicroregionID != nil && !isUUID(*request.MicroregionID)) {
			return command, errors.New("microregion_id, name o dane_code es inválido")
		}
	case "municipalities":
		command.DaneCode, command.ParentID = request.DaneCode, request.DepartmentID
		if request.Name != nil && len(*request.Name) > 150 {
			return command, errors.New("name es inválido")
		}
		if (create && (request.DaneCode == nil || request.DepartmentID == nil)) || (request.DaneCode != nil && (*request.DaneCode == "" || len(*request.DaneCode) > 8)) || (request.DepartmentID != nil && !isUUID(*request.DepartmentID)) {
			return command, errors.New("department_id, name o dane_code es inválido")
		}
	case "training-centers":
		command.Code, command.ParentID = request.CenterCode, request.MunicipalityID
		command.Address, command.Phone = request.Address, request.Phone
		if (create && (request.CenterCode == nil || *request.CenterCode == "" || request.MunicipalityID == nil)) || (request.CenterCode != nil && (*request.CenterCode == "" || len(*request.CenterCode) > 10)) || (request.MunicipalityID != nil && !isUUID(*request.MunicipalityID)) {
			return command, errors.New("municipality_id, center_code o name es inválido")
		}
	case "institutional-units":
		command.ParentID, command.UnitType = request.TrainingCenterID, request.UnitType
		if (create && (request.TrainingCenterID == nil || request.UnitType == nil)) || (request.TrainingCenterID != nil && !isUUID(*request.TrainingCenterID)) || (request.UnitType != nil && !validUnitType(*request.UnitType)) {
			return command, errors.New("training_center_id o unit_type es inválido")
		}
	}
	if !create && command.Name == nil && command.Code == nil && command.DaneCode == nil && command.ParentID == nil && command.Address == nil && command.Phone == nil && command.UnitType == nil && command.IsActive == nil {
		return command, errors.New("cuerpo de solicitud inválido")
	}
	return command, nil
}

func (h *Handler) validateParameterUpdate(r *http.Request, id string, command *application.Command) error {
	payload, err := h.queries.Get(r.Context(), "parameters", id)
	if err != nil {
		return err
	}
	var current struct {
		Value     string `json:"value"`
		ValueType string `json:"value_type"`
	}
	if err := json.Unmarshal(payload, &current); err != nil {
		return err
	}
	value, valueType := current.Value, current.ValueType
	if command.Value != nil {
		value = *command.Value
	}
	if command.ValueType != nil {
		valueType = *command.ValueType
	}
	if !validParameterValue(value, valueType) {
		return errInvalidParameterValue
	}
	return nil
}

func validUnitType(value string) bool {
	return value == "ACADEMIC" || value == "ADMINISTRATIVE" || value == "MIXED"
}
func validValueType(value string) bool {
	return value == "integer" || value == "string" || value == "boolean" || value == "json"
}
func validParameterValue(value, valueType string) bool {
	switch valueType {
	case "integer":
		_, err := strconv.ParseInt(value, 10, 64)
		return err == nil
	case "string":
		return true
	case "boolean":
		return value == "true" || value == "false"
	case "json":
		return json.Valid([]byte(value))
	default:
		return false
	}
}
func pagination(r *http.Request) (int, int, error) {
	page, err := boundedInt(r.URL.Query().Get("page"), 1, 1, 1<<31-1)
	if err != nil {
		return 0, 0, err
	}
	size, err := boundedInt(r.URL.Query().Get("page_size"), 20, 1, 100)
	return page, size, err
}
func boundedInt(raw string, fallback, min, max int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("valor de paginación inválido")
	}
	return value, nil
}
func filters(r *http.Request, resource resource) (map[string]string, error) {
	values := map[string]string{}
	for key, f := range resource.filters {
		raw, ok := r.URL.Query()[key]
		if !ok {
			continue
		}
		if len(raw) != 1 || !validFilter(raw[0], f) {
			return nil, fmt.Errorf("filtro %s inválido", key)
		}
		values[key] = raw[0]
	}
	return values, nil
}
func validFilter(value string, f filter) bool {
	switch f.kind {
	case "uuid":
		return isUUID(value)
	case "bool":
		return value == "true" || value == "false"
	case "unit_type":
		return value == "ACADEMIC" || value == "ADMINISTRATIVE" || value == "MIXED"
	case "value_type":
		return validValueType(value)
	default:
		return value != "" && len(value) <= f.max
	}
}
func sort(r *http.Request, resource resource) (string, error) {
	raw := r.URL.Query().Get("sort")
	if raw == "" {
		return "id ASC", nil
	}
	field, direction, ok := strings.Cut(raw, ":")
	column, exists := resource.sortFields[field]
	if !ok || !exists || (direction != "asc" && direction != "desc") {
		return "", errors.New("sort inválido")
	}
	return column + " " + strings.ToUpper(direction), nil
}
func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
func pages(total, size int) int {
	if total == 0 {
		return 0
	}
	return (total + size - 1) / size
}
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
func badRequest(w http.ResponseWriter, r *http.Request, err error) {
	security.WriteError(w, r, http.StatusBadRequest, "BAD_REQUEST", err.Error())
}
func unavailable(w http.ResponseWriter, r *http.Request) {
	security.WriteError(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "No es posible consultar los datos de referencia en este momento.")
}
