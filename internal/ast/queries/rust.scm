; Rust declarations. Capture contract: see go.scm and languages.go.
;   @trait  the trait of an `impl Trait for Type` block

(source_file
  (use_declaration) @import)

(source_file
  (function_item
    name: (identifier) @name) @def.function)

(source_file
  (struct_item
    name: (type_identifier) @name) @def.struct)

(source_file
  (enum_item
    name: (type_identifier) @name) @def.enum)

(source_file
  (trait_item
    name: (type_identifier) @name) @def.trait)

(source_file
  (type_item
    name: (type_identifier) @name) @def.type)

(source_file
  (const_item
    name: (identifier) @name) @def.const)

(source_file
  (static_item
    name: (identifier) @name) @def.var)

(source_file
  (impl_item
    trait: (_)? @trait
    type: (_) @owner) @def.impl)

(source_file
  (impl_item
    trait: (_)? @trait
    type: (_) @owner
    body: (declaration_list
      (function_item
        name: (identifier) @name) @def.method)))

(source_file
  (trait_item
    name: (type_identifier) @owner
    body: (declaration_list
      [(function_item
         name: (identifier) @name)
       (function_signature_item
         name: (identifier) @name)] @def.method)))

; axum route registration: .route("/path", get(handler))
(call_expression
  function: (field_expression
    field: (field_identifier) @route.method)
  arguments: (arguments
    .
    (string_literal) @route.path
    .
    (_) @route.arg2)
  (#eq? @route.method "route")) @route.call
