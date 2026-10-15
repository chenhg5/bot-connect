# 统计 issues.json：各状态的任务数、没人负责的任务
import json, sys, collections
items = json.load(open(sys.argv[1] if len(sys.argv) > 1 else "issues.json"))
by_state = collections.Counter(i["state"] for i in items)
print("各状态：", dict(sorted(by_state.items())))
print("没人负责：", sorted(i["id"] for i in items if not i.get("assignee")))
