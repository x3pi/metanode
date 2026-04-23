import os
import re
import sys
from collections import defaultdict
from datetime import datetime

# ANSI Color codes for premium look
class Colors:
    HEADER = '\033[95m'
    OKBLUE = '\033[94m'
    OKCYAN = '\033[96m'
    OKGREEN = '\033[92m'
    WARNING = '\033[93m'
    FAIL = '\033[91m'
    ENDC = '\033[0m'
    BOLD = '\033[1m'
    UNDERLINE = '\033[4m'

def analyze_logs(log_dir):
    # Regex patterns
    # [INFO][Apr 23 04:02:36]  📤 BatchSubmit [Batch 9951f3f9]
    submit_pattern = re.compile(r'\[INFO\]\[(.*?)\]\s+📤\s+BatchSubmit\s+\[Batch\s+([a-f0-9]+)\]')
    # [INFO][Apr 23 04:21:03]  🧹 [Resweeper] Batch 9951f3f9 HOÀN THÀNH
    complete_pattern = re.compile(r'\[INFO\]\[(.*?)\]\s+🧹\s+\[Resweeper\]\s+Batch\s+([a-f0-9]+)\s+HOÀN THÀNH')
    # [INFO][Apr 23 04:21:03]  [Resweeper] rescan node 127.0.0.1:4201 for batch 9951f3f9
    rescan_pattern = re.compile(r'\[INFO\]\[(.*?)\]\s+\[Resweeper\]\s+rescan\s+node\s+.*?\s+for\s+batch\s+([a-f0-9]+)')

    # Data structures
    # node_logs[filename] = { 'submitted': {id: time}, 'completed': {id: time}, 'all_ids': set() }
    node_logs = defaultdict(lambda: {'submitted': {}, 'completed': {}, 'all_ids': set()})

    print(f"{Colors.BOLD}{Colors.OKCYAN}🔍 ANALYZING LOGS IN: {log_dir}{Colors.ENDC}")
    
    log_files = []
    for root, dirs, files in os.walk(log_dir):
        for file in files:
            if file.endswith('.log') and 'Observer' in file:
                log_files.append(os.path.join(root, file))
    
    if not log_files:
        print(f"{Colors.FAIL}❌ No Observer log files found in {log_dir}{Colors.ENDC}")
        return

    for filepath in sorted(log_files):
        filename = os.path.basename(filepath)
        with open(filepath, 'r', encoding='utf-8', errors='ignore') as f:
            for line in f:
                # Check for submission
                m_sub = submit_pattern.search(line)
                if m_sub:
                    time_str, batch_id = m_sub.groups()
                    node_logs[filename]['submitted'][batch_id] = time_str
                    node_logs[filename]['all_ids'].add(batch_id)
                    continue
                
                # Check for rescan (also counts as "known" batch)
                m_rescan = rescan_pattern.search(line)
                if m_rescan:
                    time_str, batch_id = m_rescan.groups()
                    if batch_id not in node_logs[filename]['submitted']:
                        node_logs[filename]['submitted'][batch_id] = time_str # Fallback time
                    node_logs[filename]['all_ids'].add(batch_id)
                    continue

                # Check for completion
                m_comp = complete_pattern.search(line)
                if m_comp:
                    time_str, batch_id = m_comp.groups()
                    node_logs[filename]['completed'][batch_id] = time_str
                    node_logs[filename]['all_ids'].add(batch_id)

    # Report
    print(f"\n{Colors.BOLD}{'NODE (LOG FILE)':<20} | {'SUBMITTED':<10} | {'COMPLETED':<10} | {'PENDING':<10} | {'SUCCESS %':<10}{Colors.ENDC}")
    print("-" * 75)

    total_sub = 0
    total_comp = 0
    all_pending_details = []

    for filename in sorted(node_logs.keys()):
        data = node_logs[filename]
        submitted = len(data['submitted'])
        completed = len(data['completed'])
        pending = submitted - completed
        
        total_sub += submitted
        total_comp += completed
        
        success_pct = (completed / submitted * 100) if submitted > 0 else 0
        color = Colors.OKGREEN if pending == 0 else Colors.WARNING
        if success_pct < 50 and submitted > 0: color = Colors.FAIL

        print(f"{filename:<20} | {submitted:<10} | {completed:<10} | {color}{pending:<10}{Colors.ENDC} | {color}{success_pct:>8.1f}%{Colors.ENDC}")

        # Track pending batch IDs
        if pending > 0:
            pending_ids = set(data['submitted'].keys()) - set(data['completed'].keys())
            for pid in sorted(list(pending_ids)):
                all_pending_details.append((filename, pid, data['submitted'][pid]))

    print("-" * 75)
    total_pending = total_sub - total_comp
    total_success = (total_comp / total_sub * 100) if total_sub > 0 else 0
    print(f"{Colors.BOLD}{'TOTAL':<20} | {total_sub:<10} | {total_comp:<10} | {total_pending:<10} | {total_success:>8.1f}%{Colors.ENDC}")

    if all_pending_details:
        print(f"\n{Colors.BOLD}{Colors.FAIL}❌ PENDING BATCHES DETAILS:{Colors.ENDC}")
        print(f"{'NODE':<20} | {'BATCH ID':<15} | {'SUBMITTED AT'}")
        print("-" * 55)
        # Show first 30 pending items to avoid flooding
        for node, bid, time in all_pending_details[:30]:
            print(f"{node:<20} | {Colors.WARNING}{bid:<15}{Colors.ENDC} | {time}")
        
        if len(all_pending_details) > 30:
            print(f"... and {len(all_pending_details) - 30} more pending batches.")

        # Save to file
        # with open("pending_batches.txt", "w") as pf:
        #     for node, bid, time in all_pending_details:
        #         pf.write(f"{node} | {bid} | {time}\n")
        # print(f"\n{Colors.OKBLUE}💾 All pending Batch IDs saved to 'pending_batches.txt'{Colors.ENDC}")
        
    # GENERATE MARKDOWN REPORT
    md_content = "# Báo cáo Thống kê Batch Logs\n\n"
    md_content += "## 📊 Tổng quan\n\n"
    md_content += "| NODE (LOG FILE) | SUBMITTED | COMPLETED | PENDING | SUCCESS % |\n"
    md_content += "| :--- | :--- | :--- | :--- | :--- |\n"
    
    for filename in sorted(node_logs.keys()):
        data = node_logs[filename]
        sub = len(data['submitted'])
        comp = len(data['completed'])
        pend = sub - comp
        pct = (comp / sub * 100) if sub > 0 else 0
        md_content += f"| **{filename}** | {sub} | {comp} | **{pend}** | {pct:.1f}% |\n"
        
    md_content += f"| **TOTAL** | **{total_sub}** | **{total_comp}** | **{total_pending}** | **{total_success:.1f}%** |\n\n"
    
    md_content += "## ❌ Chi tiết Batch Pending (Bị kẹt)\n\n"
    
    # Group pending by node
    pending_by_node = defaultdict(list)
    for node, bid, time in all_pending_details:
        pending_by_node[node].append((bid, time))
        
    for node in sorted(pending_by_node.keys()):
        md_content += f"### 📝 {node}\n\n"
        md_content += f"**Tổng số kẹt:** {len(pending_by_node[node])}\n\n"
        md_content += "| BATCH ID | SUBMITTED AT |\n"
        md_content += "| :--- | :--- |\n"
        for bid, time in pending_by_node[node]:
            md_content += f"| `{bid}` | {time} |\n"
        md_content += "\n"
        
    with open("report.md", "w") as mf:
        mf.write(md_content)
    print(f"{Colors.OKGREEN}📄 Markdown report generated: 'report.md'{Colors.ENDC}")

    print(f"\n{Colors.OKCYAN}✨ Analysis Complete.{Colors.ENDC}")

if __name__ == "__main__":
    path = "../../observer/logs"
    if len(sys.argv) > 1:
        path = sys.argv[1]
    
    analyze_logs(path)
