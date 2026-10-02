# Submission API and Usage

YexJudge exposes an asynchronous submission API and a bounded synchronous convenience endpoint:

- `POST /submissions` creates a submission and returns its ID.
- `GET /submissions/{id}` fetches the current status and final result.
- `POST /submit` creates a submission and waits up to `SUBMIT_TIMEOUT_MS` (10 seconds by default). If it times out, it returns `202` with the submission ID and a `Location` header.
- `POST /judge` is a compatibility alias for submission creation.

The main submission path is C++. Conventional stdin/stdout submissions are also supported for C, Python, Go, and Java. Metadata-driven Function and Class modes currently support C++ only.

## Conventional stdin/stdout submissions

Send source code, test cases, and execution limits to `POST /submissions`. This C++ example adds two numbers:

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "cpp",
    "sourceCode": "#include <bits/stdc++.h>\nusing namespace std;\nint main(){ long long a,b; cin >> a >> b; cout << a + b << \"\\n\"; }",
    "testCases": [
      {
        "id": 1,
        "input": "4 7",
        "expectedOutput": "11"
      }
    ],
    "limits": {
      "timeLimitMs": 1000,
      "memoryLimitMb": 128
    }
  }'
```

Python is supported for conventional stdin/stdout submissions as well:

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "python",
    "sourceCode": "a, b = map(int, input().split())\nprint(a + b)",
    "testCases": [
      {
        "id": 1,
        "input": "4 7",
        "expectedOutput": "11"
      }
    ],
    "limits": {
      "timeLimitMs": 1000,
      "memoryLimitMb": 128
    }
  }'
```

## C++ Function Mode

Function Mode accepts a C++ `class Solution` and function metadata, then generates a hidden driver. For example, this request submits a `twoSum` implementation:

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "cpp",
    "sourceCode": "class Solution {\npublic:\n    vector<int> twoSum(vector<int>& nums, int target) {\n        unordered_map<int, int> seen;\n        for (int i = 0; i < nums.size(); i++) {\n            int need = target - nums[i];\n            if (seen.count(need)) return {seen[need], i};\n            seen[nums[i]] = i;\n        }\n        return {};\n    }\n};",
    "function": {
      "name": "twoSum",
      "returnType": "vector<int>",
      "params": [
        { "name": "nums", "type": "vector<int>&" },
        { "name": "target", "type": "int" }
      ]
    },
    "testCases": [
      { "id": 1, "args": [[2, 7, 11, 15], 9], "expected": [0, 1] },
      { "id": 2, "args": [[3, 2, 4], 6], "expected": [1, 2] }
    ],
    "limits": {
      "timeLimitMs": 1000,
      "memoryLimitMb": 128
    }
  }'
```

Function Mode also supports registered runtime types such as `TreeNode*`. This example constructs a tree from the JSON argument and compares the scalar result:

```bash
curl -X POST http://localhost:8080/submissions \
  -H "Content-Type: application/json" \
  -d '{
    "language": "cpp",
    "sourceCode": "class Solution {\npublic:\n    int maxPathSum(TreeNode* root) {\n        int maxSum = INT_MIN;\n        pathSum(root, maxSum);\n        return maxSum;\n    }\n    int pathSum(TreeNode* root, int& maxSum) {\n        if (!root) return 0;\n        int left = max(0, pathSum(root->left, maxSum));\n        int right = max(0, pathSum(root->right, maxSum));\n        maxSum = max(maxSum, left + right + root->val);\n        return root->val + max(left, right);\n    }\n};",
    "function": {
      "name": "maxPathSum",
      "returnType": "int",
      "params": [{ "name": "root", "type": "TreeNode*" }]
    },
    "testCases": [
      { "id": 1, "args": [[-10, 9, 20, null, null, 15, 7]], "expected": 42 },
      { "id": 2, "args": [[-3]], "expected": -3 }
    ],
    "limits": {
      "timeLimitMs": 1000,
      "memoryLimitMb": 128
    }
  }'
```

Supported C++ Function Mode types include `int`, `long long`, `double`, `bool`, `string`, recursive `vector<T>` and `optional<T>` values, and the registered `ListNode*`, `TreeNode*`, `RandomListNode*`, random-pointer `Node*`, and `GraphNode*` types. Reference parameters such as `vector<int>&` and `const vector<int>&` are accepted. Identity-sensitive metadata can declare generic `disjoint` or `same_as` postconditions; raw memory addresses are never exposed.

## C++ Class Mode

Class Mode constructs a user class and runs a metadata-defined sequence of operations. The contract is generic and supports stateful designs without problem-specific drivers. A testcase declares `constructorArgs`, `operations`, and one expected result per operation:

```json
{
  "mode": "class",
  "class": {
    "name": "Counter",
    "constructor": { "params": [{ "name": "initial", "type": "int" }] },
    "operations": [
      { "name": "add", "returnType": "void", "params": [{ "name": "amount", "type": "int" }] },
      { "name": "get", "returnType": "int", "params": [] }
    ]
  },
  "testCases": [
    {
      "id": 1,
      "constructorArgs": [3],
      "operations": [
        { "name": "add", "args": [4] },
        { "name": "get", "args": [] }
      ],
      "expected": [null, 7]
    }
  ]
}
```

A full LRU Cache request can be sent to `POST /submit` (the synchronous endpoint). Start the server first, then run this in Bash:

```bash
curl -sS -X POST http://localhost:8080/submit \
  -H 'Content-Type: application/json' \
  --data-binary @- <<'JSON'
{
  "language": "cpp",
  "mode": "class",
  "sourceCode": "class LRUCache {\n    int capacity;\n    list<pair<int, int>> items;\n    unordered_map<int, list<pair<int, int>>::iterator> index;\npublic:\n    LRUCache(int capacity) : capacity(capacity) {}\n\n    int get(int key) {\n        auto found = index.find(key);\n        if (found == index.end()) return -1;\n        items.splice(items.begin(), items, found->second);\n        return found->second->second;\n    }\n\n    void put(int key, int value) {\n        if (capacity <= 0) return;\n        auto found = index.find(key);\n        if (found != index.end()) {\n            found->second->second = value;\n            items.splice(items.begin(), items, found->second);\n            return;\n        }\n        items.emplace_front(key, value);\n        index[key] = items.begin();\n        if (static_cast<int>(index.size()) > capacity) {\n            auto leastRecent = prev(items.end());\n            index.erase(leastRecent->first);\n            items.pop_back();\n        }\n    }\n};",
  "class": {
    "name": "LRUCache",
    "constructor": { "params": [{ "name": "capacity", "type": "int" }] },
    "operations": [
      { "name": "put", "returnType": "void", "params": [{ "name": "key", "type": "int" }, { "name": "value", "type": "int" }] },
      { "name": "get", "returnType": "int", "params": [{ "name": "key", "type": "int" }] }
    ]
  },
  "testCases": [
    {
      "id": 1,
      "constructorArgs": [2],
      "operations": [
        { "name": "put", "args": [1, 1] },
        { "name": "put", "args": [2, 2] },
        { "name": "get", "args": [1] },
        { "name": "put", "args": [3, 3] },
        { "name": "get", "args": [2] },
        { "name": "put", "args": [4, 4] },
        { "name": "get", "args": [1] },
        { "name": "get", "args": [3] },
        { "name": "get", "args": [4] }
      ],
      "expected": [null, null, 1, null, -1, null, -1, 3, 4]
    }
  ],
  "limits": { "timeLimitMs": 1000, "memoryLimitMb": 128 }
}
JSON
```

A completed request returns a `finished` result with status `accepted`. If `/submit` reaches its bounded wait first, it returns a submission ID; poll `GET /submissions/{id}`.

## Fetching results

Use the ID returned by `POST /submissions`:

```bash
curl http://localhost:8080/submissions/1780000000000000000
```

The submission status is `queued` or `running` while processing, then `finished` or `failed`. Example accepted response:

```json
{
  "id": "1780000000000000000",
  "status": "finished",
  "result": {
    "status": "accepted",
    "runtimeMs": 12
  }
}
```

Result statuses include `accepted`, `wrong_answer`, `time_limit_exceeded`, `runtime_error`, `compilation_error`, `memory_limit_exceeded`, `validation_error`, `output_limit_exceeded`, and `infrastructure_error`.

Submissions are claimed with a lease. Expired attempts are retried up to `QUEUE_MAX_ATTEMPTS`, then recorded as failed with an infrastructure error. Recovery runs at startup and periodically so a crashed worker does not strand a submission in `running`.

## Supported languages

Use these `language` values for stdin/stdout mode:

- `c`
- `cpp`
- `python`
- `go` (legacy stdin/stdout support)
- `java`

The LeetCode-style Function/Class system currently has a C++ backend only. C, Python, Go, and Java support conventional stdin/stdout submissions; Function/Class backends for other languages are not currently supported.
