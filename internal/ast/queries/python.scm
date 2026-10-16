; Python declarations. Capture contract: see go.scm and languages.go.
;   @decorated  the decorated_definition wrapper, whose decorators are kept

(module
  (import_statement) @import)

(module
  (import_from_statement) @import)

(module
  (function_definition
    name: (identifier) @name) @def.function)

(module
  (decorated_definition
    definition: (function_definition
      name: (identifier) @name) @def.function) @decorated)

(module
  (class_definition
    name: (identifier) @name) @def.class)

(module
  (decorated_definition
    definition: (class_definition
      name: (identifier) @name) @def.class) @decorated)

(class_definition
  name: (identifier) @owner
  body: (block
    (function_definition
      name: (identifier) @name) @def.method))

(class_definition
  name: (identifier) @owner
  body: (block
    (decorated_definition
      definition: (function_definition
        name: (identifier) @name) @def.method) @decorated))

; FastAPI and Flask route decorators: @app.get("/path"), @router.post("/path"),
; @app.route("/path", methods=[...])
(decorator
  (call
    function: (attribute
      object: (identifier) @route.recv
      attribute: (identifier) @route.method)
    arguments: (argument_list
      .
      (string) @route.path) @route.args)
  (#match? @route.method "^(get|post|put|patch|delete|route|api_route)$")) @route.call
