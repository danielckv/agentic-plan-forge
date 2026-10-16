; Go declarations. Capture contract shared by every language (see languages.go):
;   @def.<kind>   the declaration node (outermost node of the declaration)
;   @name         the declared identifier
;   @owner        the owning type of a member
;   @import       an import statement node
;   @route.*      parts of a route registration call (evidence only)

(source_file
  (function_declaration
    name: (identifier) @name) @def.function)

(source_file
  (method_declaration
    receiver: (parameter_list
      (parameter_declaration
        type: (_) @owner))
    name: (field_identifier) @name) @def.method)

; struct, interface and other named types; the grammar refines the kind from the
; type node (symbols.go).
(source_file
  (type_declaration
    (type_spec
      name: (type_identifier) @name
      type: (_) @type) @def.type))

(source_file
  (type_declaration
    (type_alias
      name: (type_identifier) @name
      type: (_) @type) @def.type))

(source_file
  (const_declaration
    (const_spec
      name: (identifier) @name) @def.const))

(source_file
  (var_declaration
    (var_spec
      name: (identifier) @name) @def.var))

(source_file
  (var_declaration
    (var_spec_list
      (var_spec
        name: (identifier) @name) @def.var)))

(import_spec) @import

; http.HandleFunc, mux.Handle, and gin/echo/chi verb methods.
(call_expression
  function: (selector_expression
    operand: (_) @route.recv
    field: (field_identifier) @route.method)
  arguments: (argument_list
    .
    (interpreted_string_literal) @route.path)
  (#match? @route.method "^(Handle|HandleFunc|GET|POST|PUT|PATCH|DELETE|Get|Post|Put|Patch|Delete)$")) @route.call
