import requests
import time
from oracle import get_stock_data

# Backend connection settings
BASE_URL = "http://127.0.0.1:8000/api"
# The node needs a staff (superuser) account to fetch all orders
NODE_USER = "nw"
NODE_PASS = "1234"

def run_node():
    print("Execution Node started, scanning for orders...")

    # infinite loop — the node always listens for new work
    while True:
        try:
            # 1. fetch all orders from the API (using Basic Auth for simplicity)
            response = requests.get(f"{BASE_URL}/orders/", auth=(NODE_USER, NODE_PASS))

            if response.status_code == 200:
                orders = response.json()
                # filter only orders waiting for approval
                submitted_orders = [o for o in orders if o['status'] == 'SUBMITTED']

                for order in submitted_orders:
                    print(f"Pending order found — ID: {order['id']}, stock: {order['stock']}")

                    # 2. query the Oracle for a fresh price and timestamp
                    oracle_data = get_stock_data(order['stock'])

                    if "error" in oracle_data:
                        print(f"Oracle error: {oracle_data['error']}")
                        continue

                    # 3. send execution approval to Django (with price and timestamp)
                    payload = {
                        "execution_price": oracle_data['execution_price'],
                        "timestamp": oracle_data['timestamp']
                    }

                    print(f"Sending execution approval at price {oracle_data['execution_price']}...")

                    exec_response = requests.post(
                        f"{BASE_URL}/orders/{order['id']}/execute_order/",
                        json=payload,
                        auth=(NODE_USER, NODE_PASS)
                    )

                    if exec_response.status_code == 200:
                        print(f"Order {order['id']} approved and executed successfully.\n")
                    else:
                        print(f"Backend error: {exec_response.text}\n")

        except requests.exceptions.ConnectionError:
            print("Cannot connect to Django. Is the server (runserver) running?")

        # wait 5 seconds before the next scan to avoid flooding the server
        time.sleep(5)

if __name__ == "__main__":
    run_node()
