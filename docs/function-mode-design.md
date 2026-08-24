# YexJudge Harness and Execution Semantics Design

Status: **approved design baseline**

This document defines the architecture for extending YexJudge beyond simple return-value function calls. It intentionally does not contain an implementation plan for any individual LeetCode problem.

The central model is:

```text
execution mode + language backend + recursive type contract + observations
```

Algorithm names and algorithmic categories are not part of the judge architecture.

---

## 1. Goals

YexJudge should behave like a language runtime and code generator. A new problem should normally require only:

1. problem metadata in YexCode
2. testcases in YexCode

YexJudge should not need to know whether user code implements dynamic programming, greedy logic, BFS, tree DP, hashing, or any other algorithmic category.

YexJudge should support:

- standard input/output execution
- one-shot function invocation
- class construction and operation sequences
- future interactive protocols
- separate SQL and shell runtimes
- recursive container types
- hidden runtime types such as linked-list nodes, tree nodes, graph nodes, and API objects
- mutation, aliasing, identity, and structural observations where the execution contract requires them

The existing queue, worker, compiler, sandbox, and verdict pipeline should remain shared by all non-specialized modes.

### Initial implementation scope

The first complete Function Mode implementation is intentionally C++ only. The contracts and orchestration are designed to be reusable, but each language requires its own source-generation and runtime-emission backend. We will not duplicate the core judge work for each language now.

The near-term order is:

1. complete C++ custom runtime types and C++ Class Mode
2. stabilize the shared contracts and comparison behavior
3. add Python and Java backends only after the C++ roadmap is complete and a concrete platform requirement exists

The existing Go stdin/stdout backend is treated as a legacy compatibility path. No Go Function/Class backend is planned; whether the Go option should be removed is a separate product decision after the C++ roadmap.

## 2. Non-goals

The judge must not contain:

- a generator named after a problem
- an algorithm-category registry
- special handling for Two Sum, Copy List with Random Pointer, LRU Cache, or any other named problem
- assumptions that every submission has one function call
- assumptions that every result is a scalar JSON value
- raw memory addresses in client-provided expected output

A custom runtime type is an architectural concern. The algorithm using that type is not.

---

## 3. Core architectural axes

The system has four independent axes.

### 3.1 Execution mode

The mode defines the lifecycle of a submission.

Examples:

```text
stdin/stdout:
    stage source
    optionally compile
    run with input
    compare stdout

function:
    construct arguments
    invoke one member function
    observe return value and declared side effects

class:
    construct an object
    execute a sequence of operations
    observe each operation and/or final state

interactive:
    run a protocol driver with hidden APIs

SQL:
    execute against a database runtime

shell:
    execute in a shell-specific runtime
```

A mode owns lifecycle semantics, not problem semantics.

### 3.2 Language backend

The language backend knows how to emit code for C++, Java, Python, Go, and future languages. It does not know algorithm categories.

For example, the C++ backend knows how to emit:

- a C++ declaration
- a C++ object construction expression
- a C++ function invocation
- a C++ serializer
- a C++ identity check

The backend does not know what `copyRandomList` means.

### 3.3 Recursive type system

The type system resolves declarations such as:

```text
int
vector<int>
vector<vector<int>>
const vector<string>&
TreeNode*
vector<TreeNode*>
```

Containers should be generic. `vector<int>` and `vector<string>` must not be separate architectural concepts.

### 3.4 Observation and comparison contract

The return value is only one possible observation. A contract may also observe:

- a mutated reference parameter
- a selected prefix or slice of a parameter
- a final object state
- a graph structure
- object identity relationships
- an operation result sequence

The observation contract is supplied by YexCode metadata and interpreted generically by the harness.

---

## 4. Proposed package layout

The current primitive implementation is under `internal/judge/harness/cpp`. The long-term package boundaries should evolve toward the following structure:

```text
internal/judge/harness/
    contract.go              # mode-independent job contracts
    mode.go                  # execution-mode interfaces and registry
    observations.go          # return, mutation, structure, identity observations
    generated.go             # generated source and driver metadata

    types/
        ref.go               # recursive TypeRef representation
        parser.go            # C++ type parser and qualifier normalization
        registry.go          # type-handler registration and resolution
        schema.go            # language-neutral input/output schemas

    compare/
        canonical.go         # canonical observation format
        policy.go            # generic equality and observation policies

    modes/
        function/
            contract.go
            generator.go
        class/
            contract.go
            generator.go
        interactive/
            contract.go
            generator.go

    backends/
        cpp/
            generator.go
            serializer.go
            function.go
            class.go
            runtime_types/
                scalar.go
                vector.go
                list.go
                tree.go
                graph.go
        java/                    # deferred until the C++ roadmap is complete
        python/                  # deferred until the C++ roadmap is complete
        # Additional language backends are added only when explicitly adopted.
```

The exact directory names can change during implementation, but these dependencies should remain:

```text
judge service
    -> mode registry
        -> language backend
            -> recursive type registry
```

A type handler must not import the judge service. A mode generator must not import problem implementations.

---

## 5. Mode-independent contracts

The public job should eventually use an explicit mode. For compatibility, the current payload can continue to infer function mode from the presence of function metadata while the API migrates.

Conceptually:

```go
type Job struct {
    Language   string
    Mode       string
    SourceCode string
    Contract   ExecutionContract
    TestCases  []TestCase
    Limits     Limits
}
```

The mode should be one of a finite set registered by the server:

```text
stdin
function
class
interactive
sql
shell
```

Unknown modes must be rejected before queueing.

A mode-independent generated program should be represented separately from its source text:

```go
type GeneratedProgram struct {
    SourceCode    string
    SourceFile    string
    InputProtocol InputProtocol
    OutputProtocol OutputProtocol
}
```

This keeps the worker and executor independent from how a particular driver was generated.

---

## 6. Recursive type model

The type registry should resolve a type expression into an abstract syntax tree rather than matching complete strings.

Example:

```text
const vector<vector<int>>&
```

becomes conceptually:

```text
TypeRef {
    Base: "vector",
    Args: [
        TypeRef {
            Base: "vector",
            Args: [TypeRef{Base: "int"}],
        },
    ],
    Qualifiers: {
        Const: true,
        Reference: true,
    },
}
```

A type reference should contain:

```go
type TypeRef struct {
    Name       string
    Arguments  []TypeRef
    Const      bool
    Reference  ReferenceKind // none, lvalue, rvalue
    Pointer    bool
}
```

The parser should normalize equivalent forms:

```text
vector<int>&
const vector<int> &
std::vector<int>&
```

into the same logical type while preserving the qualifiers needed for parameter declarations.

### 6.1 Generic container handlers

A `vector<T>` handler should recursively resolve `T`:

```go
type VectorHandler struct {
    Element TypeHandler
}
```

This automatically enables:

```text
vector<vector<int>>
vector<vector<string>>
vector<TreeNode*>
optional<vector<int>>
```

provided the element handler exists. Nullable values use JSON `null` and are emitted as C++ `optional<T>` values.

The registry must not contain a manually maintained list such as:

```text
vector<int>
vector<string>
vector<vector<int>>
```

### 6.2 Type handler responsibilities

A type handler is responsible for type semantics and language-specific emission hooks, not invocation policy.

Conceptually:

```go
type TypeHandler interface {
    Resolve(ref TypeRef) error
    CanonicalName(ref TypeRef) string
    ValidateValue(ref TypeRef, raw json.RawMessage) error

    EmitSupport(ctx *SupportContext) error
    EmitInput(ctx *InputContext, value json.RawMessage, name string) (Binding, error)
    EmitSerialize(ctx *SerializeContext, expression string) (string, error)
    EmitDeserialize(ctx *DeserializeContext, expression string) (string, error)
}
```

Scalar and vector handlers can use simple expressions. Runtime object handlers can emit multi-line setup code and helper declarations.

For example:

```text
int:
    input: int __arg = 5;
    output: __serialize(__result)

vector<T>:
    input: recursively emit each T
    output: recursively serialize each element

RandomListNode*:
    input: allocate nodes, link next/random pointers
    output: traverse graph and serialize topology
```

The function generator should call these handlers through the interface and should not branch on type names.

---

## 7. Function Mode contract

Function Mode represents one invocation per process/testcase.

Conceptually:

```go
type FunctionContract struct {
    Name          string
    ReturnType    TypeRef
    Parameters    []ParameterContract
    Observations  []ObservationSpec
    Postconditions []PostconditionSpec
}

type ParameterContract struct {
    Name string
    Type TypeRef
}
```

For compatibility, `Observations` can default to observing the return value. A future explicit contract should allow multiple observations.

### 7.1 Stateless scalar/vector example

```json
{
  "mode": "function",
  "function": {
    "name": "maxProfit",
    "returnType": "int",
    "params": [
      { "name": "prices", "type": "vector<int>&" }
    ]
  },
  "testCases": [
    {
      "id": 1,
      "args": [[7, 1, 5, 3, 6, 4]],
      "expected": 5
    }
  ]
}
```

The same mode and generator apply to any method with this signature, regardless of algorithm.

### 7.2 Mutation observation example

For an in-place operation, metadata can declare that a parameter is observed after invocation:

```json
{
  "function": {
    "name": "removeDuplicates",
    "returnType": "int",
    "params": [
      { "name": "nums", "type": "vector<int>&" }
    ],
    "observations": [
      { "kind": "return" },
      {
        "kind": "parameter",
        "parameter": 0,
        "view": "prefix",
        "lengthFromReturn": true
      }
    ]
  },
  "testCases": [
    {
      "id": 1,
      "args": [[0, 0, 1, 1, 2]],
      "expected": {
        "return": 3,
        "parameter": {
          "0": [0, 1, 2]
        }
      }
    }
  ]
}
```

The judge does not know that this is a deduplication problem. It only applies a declared observation to a mutable vector parameter.

### 7.3 Runtime object example

A random-pointer list can be represented by indexes:

```json
{
  "mode": "function",
  "function": {
    "name": "copyRandomList",
    "returnType": "RandomListNode*",
    "params": [
      { "name": "head", "type": "RandomListNode*" }
    ],
    "postconditions": [
      {
        "kind": "disjoint",
        "subject": "return",
        "fromParameter": 0
      }
    ]
  },
  "testCases": [
    {
      "id": 1,
      "args": [
        {
          "values": [7, 13, 11, 10, 1],
          "random": [null, 0, 4, 2, 0]
        }
      ],
      "expected": {
        "values": [7, 13, 11, 10, 1],
        "random": [null, 0, 4, 2, 0]
      }
    }
  ]
}
```

The type adapter constructs the graph and serializes the graph. The generic postcondition checks that returned nodes are not aliases of the input nodes. No problem name is involved.

---

## 8. Observation and comparison model

The current implementation compares one trimmed stdout string. That can remain the transport mechanism initially, but the generated output should be a canonical observation document rather than an arbitrary value string.

Conceptually:

```json
{
  "return": 5,
  "parameters": {},
  "postconditions": {
    "0": true
  }
}
```

The canonical format must guarantee:

- stable object key order
- compact arrays
- deterministic null representation
- deterministic string escaping
- deterministic graph traversal
- no raw process-specific addresses

### 8.1 Identity rules

Identity is an internal relation, not an expected memory address.

Supported generic relations may include:

```text
same_as
 disjoint_from
 contains_only
 preserves_input
```

For a cloned graph, the comparator can verify:

```text
returned node set ∩ original node set = empty
```

The current C++ runtime adapters implement `disjoint` and `same_as` relations. They emit booleans into the canonical observation document; raw addresses are never exposed. Additional relations must be added to the type adapter contract, not to individual problem drivers.

For an in-place operation, the comparator can instead observe the mutated parameter and not impose an identity rule.

### 8.2 Structural graph comparison

A graph adapter should serialize a graph into a canonical index-based form. For a linked list with `next` and `random`, it can output:

```json
{
  "values": [7, 13, 11],
  "random": [null, 0, 2]
}
```

For a tree, it can use a canonical level-order representation. For a general graph, it can use a deterministic traversal with normalized edge indexes. The current C++ `GraphNode*` adapter uses:
The current C++ `GraphNode*` adapter uses:

```json
{
  "values": [1, 2, 3],
  "neighbors": [[1, 2], [2], [0]]
}
```

The root is node index `0`; validation rejects nodes unreachable from that root so the serialized topology is unambiguous.

The adapter owns the representation. The mode generator only asks it to observe an expression.

---

## 9. Class Mode

Class Mode must be separate from Function Mode because it has a different lifecycle:

```text
construct object
→ invoke operation 1
→ observe result 1
→ invoke operation 2
→ observe result 2
→ ...
```

A generic contract might look like this (LRU Cache is only an illustrative example, not a special case):

```json
{
  "mode": "class",
  "class": {
    "name": "LRUCache",
    "constructor": {
      "params": ["int"]
    },
    "operations": [
      {
        "name": "put",
        "params": ["int", "int"],
        "returnType": "void"
      },
      {
        "name": "get",
        "params": ["int"],
        "returnType": "int"
      }
    ]
  }
}
```

The class generator should be generic over constructors, operation names, parameter types, and return types. It must not contain an LRU Cache, Min Stack, or Trie driver.

A class testcase supplies constructor arguments, operation calls, and one expected result per operation:

```json
{
  "constructorArgs": [2],
  "operations": [
    { "name": "put", "args": [1, 7] },
    { "name": "get", "args": [1] }
  ],
  "expected": [null, 7]
}
```

The C++ Class Mode implementation emits a fresh object per testcase, invokes the declared sequence, serializes `void` results as `null`, and uses the same recursive type registry as Function Mode.

Function Mode must not hardcode a single-call assumption into shared harness interfaces, even though its first implementation can use one invocation.

---

## 10. Data flow

The proposed end-to-end flow is:

```text
HTTP request
    ↓
Decode versioned execution contract
    ↓
Validate mode, type expressions, JSON shapes, limits, and observations
    ↓
Persist complete job unchanged
    ↓
Queue and claim submission
    ↓
Select mode implementation
    ↓
Select language backend
    ↓
Resolve recursive types and emit hidden support code
    ↓
Emit user source and mode driver
    ↓
Compile if required
    ↓
Run one generated process per testcase
    ↓
Read canonical observation
    ↓
Apply declared comparison/identity policies
    ↓
Persist normal YexJudge verdict
```

The `Service` should orchestrate this flow but should not inspect individual type names or problem metadata beyond delegating to the selected mode/backend.

Validation should happen both:

- at the API boundary, to return `400 Bad Request`
- in the worker, to protect against old or manually inserted jobs

---

## 11. Extensibility rules

Adding a new problem should not modify YexJudge.

Adding a new recursive container should normally require:

1. a generic container handler, if the container itself is new
2. an element handler, if the element type is new

Adding a hidden runtime type should require:

1. a type schema and input encoding
2. a language backend emitter
3. type-specific serializer/observer logic
4. focused adapter tests

For C++, this is represented by registering a runtime adapter with literal generation, JSON validation/canonicalization, serializer support source, and optional postcondition expressions. The mode generators consume the registration contract without knowing the type name.

Adding a new execution lifecycle should require a new mode implementation:

```text
function → modes/function
class → modes/class
interactive → modes/interactive
```

The shared worker, queue, compiler, sandbox, persistence, and verdict layers should not change for a new problem.

---

## 12. Implementation phases after design approval

### Design checkpoint — complete

The execution-mode, recursive-type, canonical-observation, mutation, and identity contracts in this document are accepted and form the implementation boundary for Phase 1.

### Phase A: Contract and recursive type foundation

- Introduce `TypeRef` and a recursive parser.
- Introduce mode-independent contracts.
- Preserve the current scalar/vector API through compatibility conversion.
- Move type validation out of string switch statements.

### Phase B: Function Mode refactor

- Refactor the current C++ generator into lifecycle stages:
  - support declarations
  - input construction
  - invocation
  - observations
  - output protocol
- Make `vector<T>` recursive.
- Keep primitive/vector behavior unchanged.

### Phase C: Mutation and observation support

- Add post-invocation parameter observations.
- Support prefix/slice observations without problem-specific code.
- Add `void` return handling where observations still exist.
- Add generic comparison policies.

### Phase D: Custom runtime types — complete for initial C++ scope

- Add linked-list, tree, random-pointer, graph, and nullable type handlers.
- Add graph identity tracking and deterministic structural serialization.
- Add generic `disjoint` and `same_as` postconditions.
- Add tests for cloning, mutation, reuse, topology validation, and disjointness.

### Phase E: Additional execution modes

- C++ Class Mode is implemented generically for constructor and operation sequences.
- Add Interactive Mode only when a protocol contract is defined.
- Keep SQL and Shell as separate runtimes rather than extending C++ Function Mode.

---

## 13. Architectural success criteria

The design is successful when:

- algorithm categories never appear in YexJudge code
- `vector<vector<int>>` works without a new vector-specific registration
- a new scalar/container problem requires only YexCode metadata and testcases
- a new linked-list/tree/graph problem requires no problem-specific generator
- identity checks are generic postconditions
- in-place mutation is represented as an observation, not a special problem branch
- class-based problems use a separate mode rather than contaminating Function Mode
- new execution models are isolated behind mode interfaces
- generated programs remain testable independently of Postgres and Docker
