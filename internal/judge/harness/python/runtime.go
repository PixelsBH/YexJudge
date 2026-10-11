package python

const runtimeSource = `
import contextlib as __yexjudge_contextlib
import io as __yexjudge_io
import json as __yexjudge_json
import sys as __yexjudge_sys

class ListNode:
    def __init__(self, val=0, next=None):
        self.val, self.next = val, next

class TreeNode:
    def __init__(self, val=0, left=None, right=None):
        self.val, self.left, self.right = val, left, right

class RandomListNode:
    def __init__(self, val=0, next=None, random=None):
        self.val, self.next, self.random = val, next, random

class Node:
    def __init__(self, val=0, next=None, random=None, neighbors=None):
        self.val, self.next, self.random = val, next, random
        self.neighbors = [] if neighbors is None else neighbors

class GraphNode:
    def __init__(self, val=0, neighbors=None):
        self.val = val
        self.neighbors = [] if neighbors is None else neighbors

def __yexjudge_build_value(type_name, value):
    if value is None:
        return None
    if type_name.startswith("vector<"):
        if not isinstance(value, list):
            raise TypeError("vector metadata requires a JSON array")
        item_type = type_name[7:-1]
        return [__yexjudge_build_value(item_type, item) for item in value]
    if type_name.startswith("optional<"):
        return __yexjudge_build_value(type_name[9:-1], value)
    if type_name == "ListNode*":
        head = tail = None
        for item in value:
            node = ListNode(item)
            if head is None:
                head = node
            else:
                tail.next = node
            tail = node
        return head
    if type_name == "TreeNode*":
        if not value:
            return None
        nodes = [None if item is None else TreeNode(item) for item in value]
        root = nodes[0]
        pending = [root] if root is not None else []
        index = 1
        while pending and index < len(nodes):
            node = pending.pop(0)
            if node is None:
                continue
            node.left = nodes[index]
            index += 1
            if node.left is not None:
                pending.append(node.left)
            if index < len(nodes):
                node.right = nodes[index]
                index += 1
                if node.right is not None:
                    pending.append(node.right)
        if any(node is not None for node in nodes[index:]):
            raise ValueError("tree input contains unreachable nodes")
        return root
    if type_name in ("RandomListNode*", "Node*"):
        if not isinstance(value, dict) or not isinstance(value.get("values"), list) or not isinstance(value.get("random"), list):
            raise TypeError("random-list metadata requires values and random arrays")
        values, random = value["values"], value["random"]
        if len(values) != len(random):
            raise ValueError("random-list values and random lengths must match")
        node_type = RandomListNode if type_name == "RandomListNode*" else Node
        nodes = [node_type(item) for item in values]
        for index, node in enumerate(nodes):
            if index:
                nodes[index - 1].next = node
            random_index = random[index]
            if random_index is not None:
                node.random = nodes[random_index]
        return nodes[0] if nodes else None
    if type_name in ("GraphNode*",):
        if not isinstance(value, dict) or not isinstance(value.get("values"), list) or not isinstance(value.get("neighbors"), list):
            raise TypeError("graph metadata requires values and neighbors arrays")
        values, neighbors = value["values"], value["neighbors"]
        if len(values) != len(neighbors):
            raise ValueError("graph values and neighbors lengths must match")
        nodes = [GraphNode(item) for item in values]
        for index, row in enumerate(neighbors):
            nodes[index].neighbors = [nodes[neighbor] for neighbor in row]
        return nodes[0] if nodes else None
    return value

def __yexjudge_serialize_value(type_name, value):
    if value is None:
        return None
    if type_name.startswith("vector<"):
        item_type = type_name[7:-1]
        return [__yexjudge_serialize_value(item_type, item) for item in value]
    if type_name.startswith("optional<"):
        return __yexjudge_serialize_value(type_name[9:-1], value)
    if type_name == "ListNode*":
        result, seen = [], set()
        node = value
        while node is not None:
            if id(node) in seen:
                raise ValueError("cannot serialize a cyclic ListNode chain")
            seen.add(id(node))
            result.append(node.val)
            node = node.next
        return result or None
    if type_name == "TreeNode*":
        result, pending, seen = [], [value], set()
        while pending:
            node = pending.pop(0)
            if node is None:
                result.append(None)
                continue
            if id(node) in seen:
                raise ValueError("cannot serialize a cyclic or shared TreeNode structure")
            seen.add(id(node))
            result.append(node.val)
            pending.extend((node.left, node.right))
        while result and result[-1] is None:
            result.pop()
        return result or None
    if type_name in ("RandomListNode*", "Node*"):
        values, random, indices, seen = [], [], {}, set()
        node = value
        while node is not None:
            if id(node) in seen:
                raise ValueError("cannot serialize a cyclic random-list chain")
            seen.add(id(node))
            indices[id(node)] = len(values)
            values.append(node.val)
            random.append(node.random)
            node = node.next
        random_indices = []
        for item in random:
            if item is None:
                random_indices.append(None)
            elif id(item) not in indices:
                raise ValueError("random pointer references a node outside the list")
            else:
                random_indices.append(indices[id(item)])
        return {"values": values, "random": random_indices}
    if type_name == "GraphNode*":
        nodes, indices = [value], {id(value): 0}
        for node in nodes:
            for neighbor in node.neighbors:
                if id(neighbor) not in indices:
                    indices[id(neighbor)] = len(nodes)
                    nodes.append(neighbor)
        return {"values": [node.val for node in nodes], "neighbors": [[indices[id(neighbor)] for neighbor in node.neighbors] for node in nodes]}
    if type_name == "int" or type_name == "long long":
        return int(value)
    if type_name == "double":
        return float(value)
    if type_name == "bool":
        return bool(value)
    if type_name == "string":
        return str(value)
    return value

def __yexjudge_objects(type_name, value):
    if value is None:
        return set()
    if type_name.startswith("vector<"):
        result = set()
        item_type = type_name[7:-1]
        for item in value:
            result.update(__yexjudge_objects(item_type, item))
        return result
    if type_name.startswith("optional<"):
        return __yexjudge_objects(type_name[9:-1], value)
    if type_name in ("ListNode*", "TreeNode*", "RandomListNode*", "Node*", "GraphNode*"):
        pending, result = [value], set()
        while pending:
            node = pending.pop()
            if node is None or id(node) in result:
                continue
            result.add(id(node))
            if type_name == "ListNode*":
                pending.append(node.next)
            elif type_name == "TreeNode*":
                pending.extend((node.left, node.right))
            elif type_name in ("RandomListNode*", "Node*"):
                pending.extend((node.next, node.random))
            else:
                pending.extend(node.neighbors)
        return result
    return set()

def __yexjudge_postcondition(kind, result, other, type_name):
    if kind == "same_as":
        return result is other
    return __yexjudge_objects(type_name, result).isdisjoint(__yexjudge_objects(type_name, other))
`
