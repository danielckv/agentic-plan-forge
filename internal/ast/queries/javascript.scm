; JavaScript declarations. Capture contract: see go.scm and languages.go.
;   @value  the function node of a `const f = () => {}` style declaration

(import_statement) @import

(function_declaration
  name: (identifier) @name) @def.function

(generator_function_declaration
  name: (identifier) @name) @def.function

(class_declaration
  name: (identifier) @name) @def.class

(class_declaration
  name: (identifier) @owner
  body: (class_body
    (method_definition
      name: (_) @name) @def.method))

(lexical_declaration
  (variable_declarator
    name: (identifier) @name
    value: [(arrow_function) (function_expression)] @value)) @def.function

(variable_declaration
  (variable_declarator
    name: (identifier) @name
    value: [(arrow_function) (function_expression)] @value)) @def.function

; Express and Fastify route registration: app|router|fastify.<verb>("/path", ...)
(call_expression
  function: (member_expression
    object: (identifier) @route.recv
    property: (property_identifier) @route.method)
  arguments: (arguments
    .
    (string) @route.path)
  (#match? @route.recv "^(app|router|fastify)$")
  (#match? @route.method "^(get|post|put|patch|delete)$")) @route.call
