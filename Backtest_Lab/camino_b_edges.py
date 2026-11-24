"""CAMINO B: Busqueda de nuevos edges (Overnight, Pairs, Raschke+VIX)"""
import os, warnings
import numpy as np, pandas as pd, yfinance as yf
warnings.filterwarnings("ignore")
CACHE_DIR = "data_cache"; os.makedirs(CACHE_DIR, exist_ok=True)

def download(symbol, interval="1d"):
    key = f"{symbol.replace('=','_').replace('^','')}_{interval}_max_now"
    cache = os.path.join(CACHE_DIR, key + ".csv")
    if os.path.exists(cache): return pd.read_csv(cache, index_col=0, parse_dates=True)
    print(f"  [desc] {symbol}...", end=" ")
    df = yf.download(symbol, period="max", interval=interval, progress=False, auto_adjust=False, threads=False)
    if df is None or df.empty: print("vacio"); return pd.DataFrame()
    if isinstance(df.columns, pd.MultiIndex): df.columns = df.columns.get_level_values(0)
    df.columns = [c.lower() for c in df.columns]
    df = df[["open","high","low","close","volume"]].dropna()
    df.index.name = "date"; df.to_csv(cache); print(f"{len(df)} velas"); return df

def metrics(eq, capital, periods=252):
    r = eq.pct_change().dropna()
    if len(r) < 10: return None
    y = (eq.index[-1] - eq.index[0]).days / 365.25
    cagr = (eq.iloc[-1]/eq.iloc[0])**(1/y) - 1 if y > 0 else 0
    sr = r.mean()/r.std()*np.sqrt(periods) if r.std() > 0 else 0
    peak = eq.cummax(); dd = (eq - peak)/peak
    return {"sharpe": sr, "cagr": cagr*100, "mdd": dd.min()*100, "final": eq.iloc[-1]}

# --- EDGE 1: OVERNIGHT ---
def backtest_overnight(df, capital=10000):
    df = df.copy()
    df["ret"] = (df["open"] - df["close"].shift(1)) / df["close"].shift(1)
    df = df.dropna()
    eq = (1 + df["ret"]).cumprod() * capital
    return metrics(eq, capital)

# --- EDGE 2: PAIRS QQQ/SPY ---
def backtest_pairs(df_qqq, df_spy, capital=10000, window=20, entry_z=2.0, exit_z=0.5):
    q = df_qqq["close"].reindex(df_spy.index).dropna()
    s = df_spy["close"].reindex(q.index).dropna()
    ratio = s / q
    z = (ratio - ratio.rolling(window).mean()) / ratio.rolling(window).std()
    
    pos = 0
    equity = []
    nav = capital
    
    for i in range(window, len(z)):
        zi = z.iloc[i]
        if pd.isna(zi): continue
        
        if pos == 0:
            if zi > entry_z: pos = -1 # short SPY, long QQQ
            elif zi < -entry_z: pos = 1 # long SPY, short QQQ
        elif (pos == 1 and zi > -exit_z) or (pos == -1 and zi < exit_z):
            pos = 0
            
        if pos != 0 and i > 0:
            ret_q = q.pct_change().iloc[i]
            ret_s = s.pct_change().iloc[i]
            # 50% gross exposure en cada pata (100% total)
            pnl = pos * (ret_s - ret_q) * 0.5 
            nav *= (1 + pnl)
            
        equity.append({"date": z.index[i], "nav": nav})
        
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    return metrics(eq, capital)

# --- EDGE 3: RASCHKE + VIX ---
def backtest_raschke_vix(df, df_vix, capital=10000, risk_pct=1.0):
    # Alinear datos de QQQ y VIX
    df = df.copy()
    df["vix"] = df_vix["close"].reindex(df.index).ffill()
    
    df["ib_high"] = df["high"].shift(1); df["ib_low"] = df["low"].shift(1)
    df["ib_mid"] = (df["ib_high"]+df["ib_low"])/2
    
    # Filtrar solo dias donde el VIX de AYER sea > 18 (volatilidad alta)
    df["trade_allowed"] = df["vix"].shift(1) > 18.0
    
    sigs = []
    for i in range(1, len(df)):
        r = df.iloc[i]
        if pd.isna(r["ib_high"]): continue
        p, ih, il, im = r["close"], r["ib_high"], r["ib_low"], r["ib_mid"]
        
        # Regla Raschke original + Filtro VIX
        if r["trade_allowed"]:
            if p >= ih*0.999: sigs.append({"idx": i, "dir": 1, "entry": ih, "sl": im, "tp": ih+(ih-il)*0.5})
            elif p <= il*1.001: sigs.append({"idx": i, "dir": -1, "entry": il, "sl": im, "tp": il-(ih-il)*0.5})
            
    # Simulacion (igual que la v4)
    nav = capital; equity = []; open_t = None
    sig_by_day = {}
    for s in sigs: sig_by_day.setdefault(s["idx"], []).append(s)
    
    for i in range(len(df)):
        r = df.iloc[i]
        if open_t is not None:
            t = open_t; exit_px = None
            if t["dir"] == 1:
                if r["low"] <= t["sl"]: exit_px = t["sl"]
                elif r["high"] >= t["tp"]: exit_px = t["tp"]
            else:
                if r["high"] >= t["sl"]: exit_px = t["sl"]
                elif r["low"] <= t["tp"]: exit_px = t["tp"]
            if exit_px is None and (i-t["entry_idx"]) >= 5: exit_px = r["close"]
            if exit_px is not None:
                ex = exit_px * (1 - 0.0002 * t["dir"])
                pnl = (ex - t["entry_px"]) * t["dir"] * t["qty"] - 0.005*t["qty"]*2
                nav += pnl; open_t = None
                
        if open_t is None and i in sig_by_day:
            s = sig_by_day[i][0]
            entry_px = s["entry"] * (1 + 0.0002 * s["dir"])
            risk_dollars = nav * risk_pct/100
            sl_dist = abs(entry_px - s["sl"])
            if sl_dist > 0:
                qty = int(min(risk_dollars/sl_dist, (nav*0.15)/entry_px))
                if qty >= 1:
                    open_t = {"dir": s["dir"], "entry_px": entry_px, "sl": s["sl"], "tp": s["tp"], "qty": qty, "entry_idx": i}
        equity.append({"date": df.iloc[i]["date"], "nav": nav})
        
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    return metrics(eq, capital)

def main():
    print("="*75); print("  CAMINO B: NUEVOS EDGES"); print("="*75)
    capital = 10000

    print("\nDescargando datos...")
    qqq = download("QQQ", "1d")
    spy = download("SPY", "1d")
    vix = download("^VIX", "1d")

    print("\n[1] OVERNIGHT EDGE (Comprar cierre, vender apertura)")
    m = backtest_overnight(qqq, capital)
    if m: print(f"  Sharpe {m['sharpe']:.2f} | CAGR {m['cagr']:.2f}% | DD {m['mdd']:.2f}% | NAV ${m['final']:,.0f}")

    print("\n[2] PAIRS TRADING (QQQ vs SPY - Cointegracion)")
    m = backtest_pairs(qqq, spy, capital)
    if m: print(f"  Sharpe {m['sharpe']:.2f} | CAGR {m['cagr']:.2f}% | DD {m['mdd']:.2f}% | NAV ${m['final']:,.0f}")

    print("\n[3] RASCHKE + Filtro VIX (Solo opera si VIX > 18)")
    m = backtest_raschke_vix(qqq, vix, capital)
    if m: print(f"  Sharpe {m['sharpe']:.2f} | CAGR {m['cagr']:.2f}% | DD {m['mdd']:.2f}% | NAV ${m['final']:,.0f}")

    print("\n" + "="*75)
    print("  Si algun edge da Sharpe > 1.5, lo integramos al bot en Go.")

if __name__ == "__main__": main()